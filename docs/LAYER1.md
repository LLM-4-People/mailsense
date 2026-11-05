# Layer 1 — the local per-install classifier

Single source of truth for layer 1 (`internal/layer1`): what it learns,
from what, how it is measured, and the dev-time experiments. Aggregate
reports in `out/layer1-*.json`.

## What it is

Per triage question, a linear head over two inputs of each message:

- its **embedding** (the current vector space of the index: EmbeddingGemma
  document vectors by default, int8, optionally cut to
  `layer1.embedding.dimensions` — Matryoshka, re-normalised);
- its **causal layer-0 feature cells** (`evidence.FeatureLayout.Indices`:
  one-hot/bucketed/hashed cells of the causal signals, plus `~partial` and
  `?missing` cells; sparse 0/1).

Heads (`head_*.go`), one per question type, all minimising the weighted
soft cross-entropy −Σ W·log P(class range) with an L2 penalty on the
standardised weights (biases and cut points free):

| Type | Head | Notes |
| --- | --- | --- |
| choice | multinomial logistic (softmax) | one output per option |
| yes/no | logistic | P(yes); the decision uses the profile's cutoff |
| score | multinomial over the levels by default (`layer1.heads.score`); or cumulative logit (proportional odds, McCullagh 1980) | measured on the whole fixture: multinomial +0.83 pt mean dev accuracy [+0.37, +1.27] (request_count +10 pt). The ordinal head keeps its cut points ordered by construction: c_k = θ₀ + Σ exp(δ_j); a range target (a rule's level range) is −log(F_hi − F_lo−1), computed in log space |

Columns are standardised over the pass's rows and the standardisation is
folded into the weights (`fit.go`: w′ = w/σ, b′ = b − Σ w μ/σ), so neither
training nor inference materialises a dense matrix. The optimiser is
`internal/optim` (L-BFGS, two-loop recursion, Armijo backtracking,
curvature-skipped pairs, a steepest-descent restart when the quasi-Newton
direction fails; Nocedal & Wright 2006), deterministic: gradients are
summed per shard of `layer1.shard_rows` rows in a fixed order, so a fit
is bitwise the same whatever `layer1.workers`.

## First run without a decider

`NewPlan` reads the route key's unconfigured decider identity when
`triage.decider` is empty. A first-run install can therefore train, report
status and choose a non-decider teacher before any provider exists; the
decider is still selected strictly when an id is configured. The plan's
decider key and tolerance are the same values `routekey.go` hashes, and the
`decider` teacher source has no answers under the unconfigured key (a stale
row is dropped by the zero tolerance). The paid decider remains optional:
nothing local ever calls it.

## Teachers (data: `teachers.json`, `layer1.teachers.file`)

Each message's training target for a question comes from the first source
(lowest `precedence`) that has a valid one:

| Source | From | Default weight | Evaluates | Target |
| --- | --- | --- | --- | --- |
| `manual` | labels, source `manual` | 4 | yes | one-hot (a probability label for yes/no: soft) |
| `judge` | labels, source `judge` (the Gemma layer, later; the dev harness puts the gold TRAIN labels there) | 2 | yes | one-hot |
| `decider` | triage decisions of the configured `triage.decider` | 1 | no | soft: the answer's probabilities ^(1/T), checked against the provider's `probability_tolerance` (invalid → dropped, counted); refused or abstained → none (counted) |
| `rules` | layer-0 rules firing on the causal evidence | 0.3 | no | π on the rule's answer, 1−π spread over the rest; π = the Wilson lower bound of the rule's precision on the evaluation labels when ≥ `min_lower_bound`; a rule that fired on none teaches only with `unmeasured_precision` (null by default, lead decision); several firing rules are averaged (linear pool: they are correlated) |

Per-question overrides (`questions.<q>.<source>`: null removes, an object
replaces fields). Every problem of the file is reported at once. Refused:
layer 1 itself (decider `layer1`) and any local decider (a provider served
by a local runtime) as a teacher. Decisions are streamed and turned into
compact per-question float32 distributions as they are read (a run never
holds a mailbox's decoded answers).

The feedback learning loop uses `manual` labels and the judge's independent
reference labels as teaching examples, priors, evaluation and calibration
truth; `judge` labels also feed layer 1's configured sources as above, and
`judge_answer` (the model's own production answer) is written for audit only
and is never a feedback prompt, prior or reference.

The dev harness adds a `legacy` source (its own teachers override, not a
shipped source: it exists in the developer's fixture only): the legacy
Ondelle answers (`$FIXTURE_DIR/classifications.jsonl.gz`, fingerprint
`9c9956169b0c`) imported under their own decider key
`legacy:ondelle-snap-preview` (`archive.ImportLegacyAnswers` refuses any key
without the `legacy:` prefix or with a `/`: never a live decider's), never
mixed with live decisions, never a reason for a paid call (the
paid-decision policy). They were made from leaky legacy states (the
message's own reply and label): experiment E4 measures the effect per
question.

## Training a question (`train.go`, `trainpath.go`, `crossfit.go`, `holdout.go`)

Rule (review round L1, bugs #1): no fit that scores a row ever starts
from parameters fitted on that row's target — a warm start carries the
memorised target into the score (on pure-noise labels with more columns
than rows the old warm starts gave out-of-fold accuracy 1.000 and certified
99 % on every answer; `TestNoiseLabelsMeasureAsNoise`).

1. Evaluation labels E (sources with `eval`) split by conversation into
   the **selection** part S and the **certification** part C
   (`selective.InCertifyPart`: a digest of the conversation below
   `layer1.certify.fraction`, default 0.5; stable — a conversation never
   changes part as labels are added). C is held out of EVERY fit: no
   target of any source for its messages; it never shapes the rules' π
   (the rules are measured on S's labels only) nor the kNN prior.
2. With fewer than the requirement's `min_eval_labels`, fewer than 2
   selection labels or no certification label: one fit at
   `layer1.l2.default_log10` (the only fit an active model may warm-start:
   same spec, space, layout and shape), no calibration, unmeasured — every
   answer abstains (blocker `layer1_too_few_eval_labels`).
3. With targets besides E: the λ path (`layer1.l2.grid_log10`, largest
   first) from nil on every target but E's messages, each λ warm from the
   previous one (none saw an evaluation label), scored by its NLL on S
   (out of sample); S is cross-fitted at the chosen λ (`layer1.cv.folds`
   grouped folds; each fold trains on every target but C's and its own
   messages) from that λ's path fit. In a cold start (no target but E)
   every λ is cross-fitted, each fold warm from its own previous-λ fit.
   A fold whose training lacks a class is skipped: its labels are
   unscored (counted, `unscored`), the question is still trained.
4. The final model trains on every target but C's messages (from the
   chosen path fit; nil in a cold start).
5. Calibration (`combine.go`): z = A·ℓ^lr + B·log p^knn, A, B > 0
   (softplus), fitted on S's out-of-fold predictions by NLL — temperature
   scaling (Guo et al. 2017) when kNN is off; with calibration `none` and
   kNN on, A stays 1 and B alone is fitted. Each fitted scale has a
   log-normal prior worth `layer1.calibration.prior_labels` labels. No
   per-class bias.
6. Out-of-fold metrics on S (`metrics.go`): accuracy, within-1 and MAE
   (scores), NLL, Brier, ECE (10 bins), balanced accuracy and AUC
   (yes/no), κ, the selective-accuracy curve, AURC, and the coverage at
   each report target scanned on S (diagnostics only). The calibration is
   fitted on the same predictions: a small optimism, disclosed — it never
   reaches the certification.
7. **Certification** (`selective.Certify`): the threshold is chosen on S
   (the lowest confidence of the largest most-confident share whose point
   precision reaches `target_accuracy`) and tested ONCE on C's labels of
   the requirement's `certify_sources`, predicted by the final model
   itself: certified when the Wilson lower bound (at the effective `z`) of
   the answered precision reaches the target; the certified coverage is the
   answered share of C. With `family_wise: true` (the shipped default) the
   effective `z` is the two-sided Bonferroni quantile across the profile's
   questions: `z_eff = Φ⁻¹(1 − (1−Φ(z))/m)`, so a 0.95 target is a
   family-wise statement over the 17 questions rather than a per-question
   one (`z 1.96 → z_eff 2.9738`). This removes both the optimism of scanning every cut point
   (at a true precision of 0.989 the scan certified 0.99 in 5.3 % of
   trials, the split in 0.5 %: `TestSplitCertificationIsNotOptimistic`)
   and the gap between fold models and the model that answers (C never
   trained it: `TestCertificationLabelsNeverTrainTheCertifiedModel`).
   The model keeps its holdout (S's out-of-fold and C's predictions) so a
   label correction re-scores it without training.

   Certification also reports **class recall** (`class_recall`,
   `class_collapsed`): every classed row of C is eligible, and `answered` /
   `correct` count the rows the rule actually answered for that class. A
   class with at least three eligible labels and no answered row sets
   `class_collapsed`. This is report-only: `Certified` still follows the
   pooled rule, so a majority-only answer can certify this round while the
   report names the class it never reached; rejecting certification on a
   collapsed class is the next, separately approved rule. The route's fixed
   cascade reports every labelled certification item, including the ones it
   abstained on (they stay eligible but not answered), so a class below
   every gate is visible as collapsed rather than absent; the layer-1
   holdout carries every labeled row of C.

kNN (`knn.go`, `layer1.knn.*`, off by default): the k most similar
labelled messages dated strictly before the message, never itself nor
its conversation, weighted e^((s−1)/τ) and pooled with the class prior.

## Abstain and escalate (lead directive: "1.0 accuracy")

An answer given on its own must be right. Each question's model carries
an **operating threshold**: the certified one (step 7 above), or
`NoThreshold` (every answer abstains) when C did not certify it. Below it
the answer abstains: `engine.Answer.Abstain = "below_target"`, and an
abstained answer carries NO choice, value or confidence — only the
reason and its distribution (`probabilities`; yes/no: `no`/`yes`) for
diagnostics and the next layer (layer-0 key → layer 1 → Ondelle → Gemma →
the user), so no consumer can use it by mistake. That distribution and
the pre-blend model value are stored in `engine.Answer`, `route_answers`
and `triage_decisions` and stay diagnostic: the triage wire
(`/api/triage`, `/api/triage/{id}`) exposes only `type`, `refused`,
`choice`, `number`, `confidence` and `abstain`. The learned prior is
read-time only: `Priors.blend` computes it into `taxonomy.Value.Prior`,
`engine.Answer` has no prior field, and it is never persisted.
Confidence has one definition (`internal/selective`, also used by `feedback` and
`taxonomy.Uncertain`): the probability of the class chosen (yes/no: of
the side the profile's cutoff chooses; a stated confidence for a
decider's choice or score).

What the gate needs: C must answer n = ⌈z_eff²·t/(1−t)⌉ labels without
an error (381 at 0.99, z 1.96; **876** at 0.99 with the shipped
family-wise `z_eff` 2.9738 over 17 questions: a perfect 240/240 has a
Wilson lower bound of only 0.984) on at least `min_coverage` of itself,
and C is `layer1.certify.fraction` of the labels: `request_labels`
defaults to max(`min_eval_labels`, ⌈n / (min_coverage × fraction)⌉). The
request is not capped: the uncorrected defaults (0.99, z 1.96, coverage
0.5, fraction 0.5) ask for 1,524 labels, and the family-wise correction
asks for 3,504. A capped request would report "nothing remains" while the
gate still needed hundreds of answers.

The labels stage and the questions stage both follow the **route
certification** (`routes[].certification`): the route is what answers. The
layer-1 verdict's certification is the training-stage diagnostic and its
`request_labels`/`remaining` fields stay diagnostic; neither stage
reinterprets them as the route's count. The labels stage follows the
route's measured precision rather than a fixed zero-error assumption: a
97 % route asks for the larger split its error rate needs, scaled by its
certified coverage (at least `min_coverage`). When the route's answers
agree with the certification labels on less than `target_accuracy` of what
they answer, no number of labels can raise that precision. The stage then
excludes the question from its progress and emits no label blocker; the
question report keeps the design's static request as its measure,
`remaining` is 0, and the questions stage reports the precision blocker
with the re-run/review remedies and no label/judge action. The same
applies when the honest search (`neededAt`, bounded at 1,000,000 labels)
cannot reach the target: it is an unfixable route, not a silent cap. The
questions stage computes its label count and actions from the same route
`LabelNeed` plan. An older stored report with no route entry falls back to
its verdict certification for compatibility, and the labels note says so.
The verdict says what it certifies: "agreement with manual and judge
labels" (`certify_sources`; a judge's labels certify agreement with the
judge).

One predicate answers: `selective.Answering` — `Certified`, and the
certification's effective coverage at least `min_coverage` — is what the
questions blocker, the calibration stage's progress and the route's
abstention gate all call, so no surface can call a route answerable while
another abstains. The effective coverage is the Wilson coverage lower bound
of a tested certification; for a report stored before that bound existed
(`Tested` false and no bound) it is the point coverage, so an old stored
report stays answerable exactly as it was. That fallback exists only for
those stored reports: every certification written now carries the bound.

Reports measure, per question and for the route (layer-0 key → layer 1
→ legacy Ondelle; see "Route" below), at each `report_targets` precision
(0.95, 0.98, 0.99, 1.00), for layer 1, legacy Ondelle (its own
probabilities as confidence) and the route:

- the **dev certification** (the honest method on dev): dev split by
  conversation with the same fraction, the threshold chosen on one part
  and tested once on the other (`selective.Certify`) — the dev table's
  "certified" numbers. 240 dev items give a certification part of ≈ 120:
  at most a Wilson bound of 0.969, so nothing is certifiable at 0.99 on
  dev by construction;
- the dev curve (the largest most-confident dev share at the target,
  scanned on dev itself: optimistic diagnostics, point estimate and
  Wilson);
- two operating points of the model's own thresholds applied to dev:
  the **point** one (chosen on its selection labels, never certified: an
  estimate, not what the app answers) and the **certified** one (what the
  app answers with: nothing unless its certification labels certified
  it);
- the errors at the 0.99 point operating point by how their gold label
  came about (`goldset.Agreement`: single, agreed, adjudicated): counts
  in the report, the per-item list only in the private runs directory.

## Route (the `calibration` stage)

The route is the app's answer. `internal/route` is pure (engine +
selective); `internal/layer1/route.go` feeds it and stores the result;
`internal/triage.UseRoute` serves it.

- **Participants and order:** layer-0 key (a rule whose measured
  precision has a Wilson lower bound ≥ `evidence.rules.min_precision`,
  with its soft distribution), then layer 1 (the out-of-fold calibrated
  distribution on selection items, the answering model's own on
  certification items), then the configured decider's stored answer
  (Ondelle). The paid answer is read from the store; the route never calls
  a provider.
- **Gates (selection labels only):** each layer's gate is
  `selective.Targets` on **all** of that layer's labelled selection
  offers, independently of the other layers. A layer whose gate is
  `NoThreshold` is not a member and cannot answer. (Measuring a lower
  layer only where no higher layer offered was the first implementation;
  it starved Ondelle and made the route equal layer 1 alone.) Layer 1's
  selection offer is the model's **out-of-fold** prediction
  (`Holdout.Select`: the fold models' calibrated probabilities, none of
  which trained on its own labels), never the final model's: the final
  model trained on every selection label outside the certification part,
  so its predictions there are in-sample and would choose an optimistic
  gate. A selection label with no out-of-fold prediction (its fold could
  not score it, or the active model never scored it) offers no layer 1 for
  selection; it is never replaced by the final model. The re-score rebuilds
  the same offers from the active model's `Holdout.Select`, and key and
  Ondelle offers are unchanged.
- **Answering (cascade):** the first member that offered and whose
  confidence reaches its gate answers. A higher member below its gate
  **falls through** to the next member. If a member offered but none
  reached its gate, the answer abstains `below_target` with the
  highest-priority member's distribution; if no member offered,
  `no_answer`; if the route is not certified, `not_certified`; if it is
  certified but its coverage lower bound misses the minimum,
  `below_coverage`. An
  abstention carries no choice, value or confidence.
- **Certification (certification labels only):** the cascade's decided
  answers on C, with the final model's layer-1 distributions, are measured
  by `selective.CertifyFixed` — no cut, gate or member is chosen on C, not
  even scanned. `Certified` and `Coverage`
  (answered/eligible, every labelled certification message in the
  denominator) are what the `questions` stage judges. A route's
  `Certification.Threshold` is 0: it has no single cut.
- **Read-time rule fallback:** when the route stores `not_certified` for a
  question, `internal/triage` may fill it from a **definitional** evidence
  rule (`evidence.Engine.RuleAnswers`). Definitional means the signal's
  semantics and the question's definition guarantee the answer (a delivery
  report is a notification; an internal sender is a colleague); an
  estimate—a question mark for a request, a named date for a deadline—is
  taught and measured, never an unmeasured answer. No rule may read a
  lexical signal: rules use structure, protocol and mailbox statistics, not
  a wordlist. The answer is deterministic, carries the rule id in `rule`,
  and is never written back: it is not a label, a route answer, a coverage
  count or a gate permission. Firing rules must agree; for a score only the
  narrowest exact level counts, so a range or a conflict answers nothing. A
  `below_coverage`, `below_target`, `no_answer`, refused or certified answer
  is never replaced. The detail and list say "automatic rule · uncertified"
  and count those answers apart from the certified total.
- **Storage:** `route_answers`, one row per message and route key, every
  question present (abstentions included), written in the layer-1
  commit's transaction. The route key is `rk1-` + a digest of every
  configuration input that can change an answer (the full profile, the
  merged teachers and requirements, the decider and its tolerance, the
  algorithm and feature versions, the merged evidence rules, the folder
  roles and every route-affecting setting), so a configuration change can
  never read another configuration's rows. The legacy 12-hex profile
  fingerprint remains the paid-decision, judge and import identity. A
  cancelled or failed pass writes no routes.
- **Stages:** `calibration` (`layer1.RouteStage`) requires a pass with a
  route for every question and `Routed == Answered`, else blocks
  `route_not_routed` with `run_layer1`. `questions` certifies the route.
- **Measured (dev, `v8-route-cascade`, 240 per question):** dev-curve
  coverage at 0.95/0.98/0.99/1.00 = 0.8483/0.8023/0.6982/0.5518 (layer 1
  alone 0.4255/0.3145/0.2556/0.2096). At 0.99 Ondelle gives 914 of 2,085
  point-op answers. Honest certification at 0.99 is still 0/17: C holds
  117 labels and needs 381 error-free answers. Point op at 0.99: 2,085
  answers, 37 errors (0.9823), never what the app answers.

## Runs, promotion, answers, stages

- `Runner.Run` (job `layer1`, one at a time: `ErrBusy`): the run is
  recorded first (a broken configuration is a recorded failed run with
  blocker `layer1_config_invalid` and its remedy — `choose_teacher` or
  `review_requirements` — never an aborted app start); every question is
  trained; each candidate is stored and becomes the question's model
  unless the active model fits the pass (same vector space, layout,
  shape, question spec digest `SpecOf`, class count, and a kNN index when
  it pools one) and the candidate's out-of-fold NLL is worse by more than
  `layer1.promote.max_nll_regression`; a question without a candidate (no
  teacher, a single class) retires its active model (it stops
  answering). Every settled model is then certified against the current
  labels, the answers are computed (decider `layer1`; a member without a
  vector of the space gets none: pending, never imputed), and
  promotions, retirements, re-scored thresholds, the answers and the
  run's report are written atomically (`store.CommitLayer1`): the answer
  rows are first streamed into a connection-private staging table outside
  any main-database transaction, then one short `BEGIN IMMEDIATE` promotes,
  retires, replays the staged rows and writes the report, so inference does
  not hold the write lock; a failure or a cancel promotes nothing and keeps
  the previous answers. The separate decisions/routes/preview streams used
  by a re-score have no inference phase to stage and keep their one
  transaction.
  Non-active models kept per question: `layer1.retain.retired_models`.
- Verdicts come from the model that answers after the run (a kept model's
  own holdout, re-scored), with the label sources certified against.
- `Runner.Rescore`: any change of an evaluation label voids the verdicts
  (the stages show "re-scoring"); the re-score certifies the active
  models' holdouts against the labels as they stand now, moves their
  thresholds and re-decides their stored answers from their own
  distributions, in one commit, without training. New labels on messages
  outside a holdout count toward a retraining.
- `AfterRuns`: a run after evidence runs, index runs that committed work
  (stored or confirmed a vector), label changes,
  layer-1 setting changes and data-file edits (`config.WatchDataFiles`
  publishes an edit of any `data_file` setting's file every
  `config.watch_interval`), only when the inputs changed: the fingerprint
  covers the settings, the merged teachers and requirements documents
  (every per-question override, `report_targets`), the whole profile, the
  decider key, the last evidence run, the vector sequence and the
  decision counts; at least `layer1.retrain.min_new_labels` changed label
  rows retrain, fewer that touch an evaluation label re-score; a broken
  configuration is recorded once per distinct problem.
- Stage `layer1`: pending until evidence and embeddings are done; blocked
  with `layer1_config_invalid` while the teachers or requirements do not
  load; running while the inputs changed (or "re-scoring the verdicts");
  blocked (subject = question) with `layer1_no_teacher` (actions
  `choose_teacher`, `run_judge`, `label_messages`),
  `layer1_too_few_labels`, `layer1_too_few_eval_labels`,
  `layer1_single_class`, `layer1_run_failed` (`review_failures`,
  `run_layer1`); done otherwise. Blocker details are plain language; a
  `measure` gives have/need (evaluation labels, effective training
  examples, certified coverage); `count` is what remains to label
  (requested − have); the paid `run_judge` carries `estimated_cost_usd`
  ("unknown" with a `cost_note` until the judge exists).
- Stage `questions`: every question must meet its requirement
  (`requirements.json`, `layer1.requirements.file`: `min_effective_train`,
  `min_eval_labels`, `target_accuracy`, `min_coverage`, `z`,
  `request_labels`, `certify_sources`); otherwise `requirement_unmet`
  with the certified coverage, what it agrees with, and the labels that
  remain. Its label count and actions come from the route's `LabelNeed`
  plan, never the layer-1 verdict's diagnostic `request_labels`. The gate
  never relaxes.

## Dev harness (`scripts/layer1-fixture.sh`)

- `embed [phases]`: real EmbeddingGemma vectors (the pinned Linux
  llama-server through the sidecar) into the private vector cache
  (`~/.cache/mailsense-layer1/vectors`, mode 700; one append-only file per
  vector space, keyed by the exact input): the experiment sample (every
  gold message + 8,000 others drawn by legacy category, seeded) in each
  E1 purpose (`document`, `classification` = `task: classification |
  query: {subject + body}`, `clustering`), then the whole fixture
  (document vectors). Reused by every later run.
- `run [experiment]`: the fixture (`$FIXTURE_DIR`, `lib.sh`:
  `~/.local/share/mailsense/fixture`, mode 700/600: personal data, never
  in the repository nor `out/`) imported into a fresh store, the cached
  vectors written into it, the evidence engine run, the legacy answers
  imported as the harness's `legacy` teacher **except every gold dev and
  test message** (E6: their conversation-mates too), the gold TRAIN labels
  as `judge` labels, `Train` run through `harnessInputs` (dependency
  injection, never a flag in production code), a guard that fails the run
  when an excluded message has any target, then the dev measurement:
  layer 1, the legacy Ondelle answers of the same messages, the majority
  class, the agreement ceiling, per language.
  Reports `out/layer1-<experiment>.json` (aggregates, and the process's
  peak RSS after training), a line in `out/layer1-dev-ledger.jsonl` per
  dev evaluation; per-item correctness and the per-item error list
  (gold ids) only under `~/.cache/mailsense-layer1/runs/` (paired
  bootstraps: `LAYER1_BASELINE=<run>`, over the sorted union of the dev
  items: deterministic).
- The gold test split is sealed (`scripts/gold-seal.sh`,
  `internal/goldset`): `Open` runs the seal planner read-only and refuses
  any file still naming a test message; `Seal` refuses a kept line naming
  a test id in any field; read once, later, by `goldset.OpenTest` with
  `GOLD_UNSEAL` naming the frozen model's digest (exclusive lock and
  ledger).

## Experiments (dev only)

Method: one change per round against the round's baseline; mean over the
17 questions of the dev accuracy and (from round v4, the primary metric)
of the dev-curve coverage at precision ≥ 0.99; 95 % paired bootstrap over
the 240 dev messages; every evaluation appended to
`out/layer1-dev-ledger.jsonl`. Stop rule: two consecutive rounds under
1 pt — met after the whole-fixture rounds.

| Finding | Outcome |
| --- | --- |
| v1 → v2: the held-out evaluation messages kept their own Ondelle answer as a target | out-of-fold ≫ dev, λ at the grid's edge; fixed (no target of any source for a held-out message) |
| E1 prompts (sample) | classification +0.56 pt [−0.42, +1.52]: document vectors kept |
| E1 inputs | no embedding −3.7 pt, no layer-0 cells −1.3 pt, 256-d −0.7 pt: both inputs, 768-d kept |
| E2 | λ grid: no effect; tolerance 1e-4: same answers at 2.5× less cost (adopted); class balancing −0.4 pt; softmax score heads +0.83 pt on the whole fixture (adopted) |
| E3 kNN | +0.8 pt on the sample, +0.4 pt (n.s.) on the whole fixture: not adopted |
| E4 teachers | cold start −10 pt; legacy off for the leak-prone questions −6 pt (they fall to the cold start); hard targets, T 2, judge weight 50: no gain |
| E5 calibration | temperature: ECE 0.091 → 0.053; its unbounded fit on one-sided labels raised NLL: prior added (adopted) |
| E6 | conversation-mates also excluded: −0.5 pt (n.s.): no leak of consequence |
| E6 again (v6) | conversation-mates excluded too: −0.20 pt [−0.54, +0.15], coverage −1.1 pt [−2.4, +1.1]: no leak through mates |
| E7 errors | layer 1's dev errors are mostly the teacher's (topic technical_integration ↔ support_issue, category work ↔ action, effort/request_count 0 → 1), plus under-predicting "yes" on reply_needed/call_to_action; at the 0.99 operating point most errors are on clear (single or agreed) gold items |
| E8 definitions | zero-shot nearest taxonomy description (local EmbeddingGemma): mean dev 0.4265 vs 0.4319 majority; helpful on topic/asks_for/has_signature, harmful on reply_needed/is_question/effort: not adopted; kept as the baseline for a generative judge |
| Data | the whole fixture (33,096 teachers vs 8,119): +0.5 pt accuracy, +4.5 pt coverage at 0.99 |

Final (v6 = the review round's fixed method, shipped defaults, whole
fixture): mean dev accuracy 0.781 vs legacy Ondelle 0.827 (v5 0.782);
dev-curve coverage at 0.99 0.256 vs 0.450; certified coverage at 0.99 —
on dev by the honest split, and in the app — 0 for layer 1, Ondelle and the
stack (≈ 120 certification items bound the Wilson bound at 0.969).

### Queued (not run yet)

- **Quoted-tail detection by the parent message** (`mail.CleanBody`,
  layer 0) — measured, deprioritised: `threads.TestFixtureQuotedTails`
  (fixture tag) finds the parent of 23,575 owner messages (In-Reply-To,
  else the conversation's latest earlier message); only 112 keep text
  after a tab-indented quote block, 30 of them end in ≥ 3 lines found in
  the parent (180 lines), and **no gold dev message** is among them, so
  dev cannot measure the change. Matching any trailing lines against the
  parent is wrong in general: 51 % of answering messages end in ≥ 3
  lines found in their parent — the sender's own repeated signature. A
  remedy must therefore be limited to text after an indented block; kept
  queued until a corpus with such messages is labelled.
- **Shared kNN neighbours**: with kNN on, answering every message costs
  one scan per message and question (≈6 ms per scan over 34,000 vectors,
  `BenchmarkKNN`); the neighbours do not depend on the question (only
  their targets do), so one scan per message would serve every question.
  Only worth building if E3 keeps kNN.
