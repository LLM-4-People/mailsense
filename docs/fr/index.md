---
layout: default
title: "Mailsense (français)"
lang: "fr"
language_switch: true
description: "Posez une question à votre courrier. Il répond par un décompte, une liste ou une réponse écrite qui cite les messages utilisés."
image:
  path: "/assets/social-preview.png"
  width: 1200
  height: 630
  alt: "Mailsense: Demandez à votre courrier. Chaque citation ouvre le message."
---

<header class="hero">
  <div class="shell hero-grid">
    <div class="hero-copy">
      <h1 class="promise" id="thesis-title">Posez une question à votre courrier. Il répond par un décompte, une liste ou une réponse écrite qui cite les messages utilisés.</h1>
      <p class="lede">Il lit le profil Outlook classique déjà ouvert sur votre PC. Ask répond depuis l&#39;index local ; une réponse écrite ne vient que d&#39;un modèle que vous avez configuré et des messages qu&#39;il peut citer. Le stockage, le classifieur et le profil appris restent sur votre disque.</p>
      <p class="actions"><a class="button" href="install.html">Installer</a><a class="quiet-link" href="first-run.html">Lire le guide du premier démarrage</a></p>
      <p class="hero-note">Installateur Windows. Sans compte, sans télémétrie.</p>
    </div>
    <div class="hero-stage">
      <figure class="app-window">
        <div class="app-bar">
          <img src="../assets/mark.svg" width="20" height="20" alt="">
          <span class="app-name">Mailsense</span>
          <span class="app-badge">Aperçu</span>
        </div>
        <img class="app-shot" src="../assets/screenshots/fr/home.png" width="1440" height="900" loading="eager" decoding="async" alt="Mailsense: Accueil">
      </figure>
      <aside class="answer-slip" aria-labelledby="slip-label">
        <div class="slip-head">
          <p class="slip-label" id="slip-label">ASK / RÉPONSE ANCRÉE</p>
          <span class="slip-meta">
            <span class="slip-sample">Exemple de réponse</span>
            <span class="chip">Auto</span>
          </span>
        </div>
        <p class="slip-question">Qui attend la proposition Northwind ?</p>
        <p class="slip-answer">Trois fils attendent encore votre réponse. Atelier Nord garde la fenêtre de livraison de jeudi jusqu&#39;à ce que vous confirmiez la proposition signée. Harbor Logistics demande quel créneau d&#39;enlèvement convient, et Studio Lumen a envoyé une facture dont le lien de paiement mène ailleurs. Les deux premiers sont des réponses ordinaires. La facture est le message à vérifier avant de payer.</p>
        <ol class="citations">
          <li>
            <span class="citation-mark" aria-hidden="true">01</span>
            <span class="citation-main">
              <span class="citation-subject">Proposition signée pour le lancement Northwind</span>
              <span class="citation-meta">Atelier Nord · 3 nov. 2026</span>
            </span>
          </li>
          <li>
            <span class="citation-mark" aria-hidden="true">02</span>
            <span class="citation-main">
              <span class="citation-subject">Créneau d&#39;enlèvement du jeudi</span>
              <span class="citation-meta">Harbor Logistics · 2 nov. 2026</span>
            </span>
          </li>
          <li>
            <span class="citation-mark" aria-hidden="true">03</span>
            <span class="citation-main">
              <span class="citation-subject">La facture 1042 demande une vérification</span>
              <span class="citation-meta">Studio Lumen · 3 nov. 2026</span>
            </span>
          </li>
        </ol>
        <p class="slip-caption">Dans l&#39;application, chaque citation ouvre le message qu&#39;elle nomme.</p>
      </aside>
    </div>
  </div>
</header>

<section class="section section-ask" aria-labelledby="ask-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="ask-title">Commencez par une question</h2>
      <p class="section-lead">Ask répond en clair à une question sur votre courrier. L&#39;écran nomme le mécanisme qui a répondu : une requête typée, une recherche ou un modèle que vous avez configuré.</p>
    </div>
    <div class="ask-bento">
      <article class="ask-card ask-card-grounding">
        <div class="card-top">
          <span class="card-number">01</span>
          <span class="card-tag">Ancrage</span>
        </div>
        <h3>Une réponse écrite cite chaque message utilisé.</h3>
        <p>Le modèle doit écrire au plus 120 mots et marque chaque affirmation par un numéro de message. Le serveur vérifie chaque marqueur contre l&#39;ensemble récupéré, refuse une réponse dont un marqueur pointe hors de cet ensemble, renumérote les marqueurs en positions de citation et ne garde une citation que si elle nomme un message réel. Quand aucune citation ne survit, les messages correspondants restent à l&#39;écran avec un avis qui nomme la cause.</p>
        <ul class="mini-cites">
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">Proposition signée pour le lancement Northwind</span>
            <span class="mini-sender">Atelier Nord</span>
          </li>
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">Créneau d&#39;enlèvement du jeudi</span>
            <span class="mini-sender">Harbor Logistics</span>
          </li>
          <li>
            <span class="mini-dot" aria-hidden="true"></span>
            <span class="mini-subject">La facture 1042 demande une vérification</span>
            <span class="mini-sender">Studio Lumen</span>
          </li>
        </ul>
      </article>
      <article class="ask-card ask-card-local">
        <div class="card-top">
          <span class="card-number">02</span>
          <span class="card-tag">Local</span>
        </div>
        <h3>Un décompte ou un classement est calculé, pas rédigé.</h3>
        <p>Un décompte, un classement et le profil d&#39;une organisation sont lus dans l&#39;index local et les faits stockés. Le nombre et les lignes viennent d&#39;une requête typée, jamais d&#39;un texte de modèle. Le mode Local n&#39;appelle jamais de modèle : le même décompte répond quand même, et une question libre renvoie les messages correspondants avec un avis qui dit que Local n&#39;appelle jamais de modèle.</p>
        <div class="mode-chips" aria-hidden="true">
          <span class="mode-chip">Auto</span>
          <span class="mode-chip is-on">Local</span>
          <span class="mode-chip">Distant</span>
        </div>
      </article>
      <article class="ask-card ask-card-trace">
        <div class="card-top">
          <span class="card-number">03</span>
          <span class="card-tag">Trace</span>
        </div>
        <h3>Une citation ouvre le message, et la réponse est conservée.</h3>
        <p>Chaque ligne de citation porte l&#39;objet, l&#39;expéditeur et la date, et mène au message qu&#39;elle nomme. Chaque réponse est conservée avec sa question, sa route, son mode, ses citations et, quand un modèle a servi, son fournisseur et son modèle. Le rail d&#39;historique la rouvre, et Effacer l&#39;historique retire toutes les réponses conservées.</p>
        <div class="mini-history">
          <span class="mini-history-dot" aria-hidden="true"></span>
          <span>Qui attend la proposition Northwind ?</span>
        </div>
      </article>
    </div>
    <div class="ask-lower">
      <div class="mode-panel">
        <h3 id="modes-title">Trois modes, une règle</h3>
        <table class="mode-table" aria-labelledby="modes-title">
          <thead><tr><th scope="col">Mode</th><th scope="col">Ce qui répond</th><th scope="col">Quand un modèle est appelé</th></tr></thead>
          <tbody>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Auto</span></th>
              <td data-label="Ce qui répond">L&#39;index local et le classement quand la question a une réponse exacte, sinon le fournisseur configuré ou les messages correspondants.</td>
              <td data-label="Quand un modèle est appelé">Seulement pour une question qu&#39;aucune intention exacte ne couvre.</td>
            </tr>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Local</span></th>
              <td data-label="Ce qui répond">L&#39;index local, le classement et le profil stocké.</td>
              <td data-label="Quand un modèle est appelé">Jamais.</td>
            </tr>
            <tr>
              <th scope="row" data-label="Mode"><span class="mode-name">Distant</span></th>
              <td data-label="Ce qui répond">Le fournisseur configuré, ancré dans les messages récupérés.</td>
              <td data-label="Quand un modèle est appelé">Pour chaque question avec des preuves récupérées. Proposé seulement quand un fournisseur est utilisable.</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="guarantee-panel">
        <p class="pull">Chaque citation ouvre le message qu&#39;elle nomme, et chaque réponse est conservée avec sa question pour être rouverte.</p>
        <p class="note">Ask est borné par <code>qa.max_messages</code>, <code>qa.retrieval_timeout</code>, <code>qa.call_timeout</code> et les deux réglages de budget. Un index en préparation garde la question et propose Réessayer.</p>
        <p class="chapter-link"><a href="ask.html">Lire le guide Ask</a></p>
      </div>
    </div>
  </div>
</section>

<section class="section section-queue" aria-labelledby="queue-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="queue-title">Trois choses qu&#39;il fait autrement</h2>
      <p class="section-lead">La file, la preuve et le verrou. Chacune est un mécanisme que vous pouvez inspecter.</p>
    </div>
    <div class="queue-layout">
      <div class="queue-panel">
        <p class="queue-label">Une file inventée. Le score suit une règle locale, pas un appel à un modèle.</p>
        <table class="queue-table">
          <thead><tr><th scope="col">Rang</th><th scope="col">Objet</th><th scope="col">Expéditeur</th><th scope="col">Action</th></tr></thead>
          <tbody>
            <tr>
              <th scope="row" class="queue-rank">01</th>
              <td class="queue-subject">Proposition signée pour le lancement Northwind</td>
              <td class="queue-sender">Atelier Nord</td>
              <td class="queue-action"><span>Décider</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">02</th>
              <td class="queue-subject">La facture 1042 demande une vérification</td>
              <td class="queue-sender">Studio Lumen</td>
              <td class="queue-action"><span>Vérifier</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">03</th>
              <td class="queue-subject">Créneau d&#39;enlèvement du jeudi</td>
              <td class="queue-sender">Harbor Logistics</td>
              <td class="queue-action"><span>Répondre</span></td>
            </tr>
            <tr>
              <th scope="row" class="queue-rank">04</th>
              <td class="queue-subject">Notes d&#39;atelier et les deux questions ouvertes</td>
              <td class="queue-sender">Morgan Ellis</td>
              <td class="queue-action"><span>Lire</span></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="queue-notes">
        <div class="queue-band">
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">01</span>
            <p class="card-tag">File</p>
            <h3>La file est ordonnée par une règle locale fixe.</h3>
            <p>Un moteur Go local ordonne chaque message à partir des signaux stockés : un message suspect ajoute 100, une décision 45, une réponse 30, les niveaux d&#39;urgence et d&#39;échéance ajoutent des poids fixes, et la date de réception ajoute au plus 12. Un score de 20 suffit pour entrer, et un signal de suspicion, de décision, de réponse, d&#39;urgence ou d&#39;échéance entre toujours. Le courrier automatisé de masse n&#39;entre jamais, quelle que soit l&#39;urgence de son texte.</p>
          </article>
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">02</span>
            <p class="card-tag">Preuve</p>
            <h3>Chaque étiquette nomme sa source.</h3>
            <p>Une décision est stockée comme des réponses à des questions nommées, avec la confiance de chaque réponse quand ce type de question en porte une et les éléments de preuve qui la soutiennent. Le détail du message montre les champs lus par le modèle. Quand un index est partiel, l&#39;écran nomme la partie manquante.</p>
          </article>
          <article class="queue-item">
            <span class="band-number" aria-hidden="true">03</span>
            <p class="card-tag">Apprentissage</p>
            <h3>Il s&#39;abstient tant que vos propres étiquettes ne l&#39;ont pas certifié.</h3>
            <p>Le classifieur local entraîne une tête par question sur vos corrections. Une question n&#39;est certifiée que lorsque la borne inférieure de Wilson de sa précision répondue atteint 0,99 sur une partition mise de côté, avec au moins 200 étiquettes d&#39;entraînement, 50 étiquettes d&#39;évaluation et une couverture de 0,5. En dessous, il s&#39;abstient, et l&#39;écran Apprentissage nomme l&#39;étape qui bloque.</p>
          </article>
        </div>
      </div>
    </div>
  </div>
</section>

<section class="section section-screens" aria-labelledby="screens-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="screens-title">Les écrans, à commencer par Ask</h2>
      <p class="section-lead">Capturés depuis l&#39;aperçu Linux avec une boîte inventée. Aucun courrier réel n&#39;apparaît ici.</p>
    </div>
    <div class="screens-panel">
      <fieldset class="screen-index" aria-describedby="screen-hint">
        <legend class="visually-hidden">Index des écrans</legend>
        <div class="screen-index-head">
          <p class="screen-index-label" aria-hidden="true">Index des écrans</p>
          <p class="screen-hint" id="screen-hint"><span class="screen-hint-pointer">Choisissez un écran. Les flèches permettent de passer d&#39;un écran à l&#39;autre.</span><span class="screen-hint-touch">Choisissez un écran.</span></p>
        </div>
        <div class="screen-list">
          <label class="screen-tab" for="screen-ask">
            <input class="screen-radio" type="radio" name="screen" id="screen-ask" checked>
            <span class="screen-num" aria-hidden="true">01</span>
            <span class="screen-name">Demander avec une réponse</span>
          </label>
          <label class="screen-tab" for="screen-home">
            <input class="screen-radio" type="radio" name="screen" id="screen-home">
            <span class="screen-num" aria-hidden="true">02</span>
            <span class="screen-name">Accueil</span>
          </label>
          <label class="screen-tab" for="screen-mail">
            <input class="screen-radio" type="radio" name="screen" id="screen-mail">
            <span class="screen-num" aria-hidden="true">03</span>
            <span class="screen-name">Courrier avec un message ouvert</span>
          </label>
          <label class="screen-tab" for="screen-teach">
            <input class="screen-radio" type="radio" name="screen" id="screen-teach">
            <span class="screen-num" aria-hidden="true">04</span>
            <span class="screen-name">Enseigner</span>
          </label>
          <label class="screen-tab" for="screen-learning">
            <input class="screen-radio" type="radio" name="screen" id="screen-learning">
            <span class="screen-num" aria-hidden="true">05</span>
            <span class="screen-name">Apprentissage</span>
          </label>
          <label class="screen-tab" for="screen-sources">
            <input class="screen-radio" type="radio" name="screen" id="screen-sources">
            <span class="screen-num" aria-hidden="true">06</span>
            <span class="screen-name">Sources</span>
          </label>
          <label class="screen-tab" for="screen-settings">
            <input class="screen-radio" type="radio" name="screen" id="screen-settings">
            <span class="screen-num" aria-hidden="true">07</span>
            <span class="screen-name">Réglages</span>
          </label>
          <label class="screen-tab" for="screen-first_run">
            <input class="screen-radio" type="radio" name="screen" id="screen-first_run">
            <span class="screen-num" aria-hidden="true">08</span>
            <span class="screen-name">Premier démarrage</span>
          </label>
        </div>
        <div class="screen-stage">
          <figure class="shot" id="shot-ask">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/ask.png" width="1440" height="900" loading="eager" decoding="async" alt="Mailsense: Demander avec une réponse">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/ask.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Demander avec une réponse</strong> Ask mène : une question et sa réponse écrite, avec chaque citation sous le texte qui ouvre le message qu&#39;elle nomme.</figcaption>
          </figure>
          <figure class="shot" id="shot-home">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/home.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Accueil">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/home.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Accueil</strong> La journée en trois colonnes : ce qui attend une réponse, ce qui attend une décision et ce qui arrive.</figcaption>
          </figure>
          <figure class="shot" id="shot-mail">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/mail.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Courrier avec un message ouvert">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/mail.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Courrier avec un message ouvert</strong> Le message choisi montre son expéditeur, son corps et les champs lus par le modèle local.</figcaption>
          </figure>
          <figure class="shot" id="shot-teach">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/teach.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Enseigner">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/teach.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Enseigner</strong> Une question à la fois. La suggestion est affichée, et votre réponse est l&#39;étiquette qui entraîne le modèle local.</figcaption>
          </figure>
          <figure class="shot" id="shot-learning">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/learning.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Apprentissage">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/learning.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Apprentissage</strong> Chaque étape indique sa progression, et l&#39;écran nomme l&#39;étape qui bloque encore la certification.</figcaption>
          </figure>
          <figure class="shot" id="shot-sources">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/sources.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Sources">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/sources.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Sources</strong> Le stockage Outlook, la boîte choisie et les dossiers que Mailsense peut lire.</figcaption>
          </figure>
          <figure class="shot" id="shot-settings">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/settings.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Réglages">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/settings.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Réglages</strong> Les réglages du quotidien d&#39;abord. Le schéma complet reste à un clic.</figcaption>
          </figure>
          <figure class="shot" id="shot-first_run">
            <div class="screen-frame">
              <img src="../assets/screenshots/fr/first-run.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Premier démarrage">
            </div>
            <a class="shot-full" href="../assets/screenshots/fr/first-run.png" target="_blank" rel="noopener">Voir en plein format</a>
            <figcaption><strong>Premier démarrage</strong> Le premier lancement écrit le marqueur d&#39;installation, nomme le répertoire de données et demande un fuseau horaire avant de lire le courrier.</figcaption>
          </figure>
        </div>
      </fieldset>
    </div>
  </div>
</section>

<section class="section section-privacy" aria-labelledby="boundary-title">
  <div class="shell">
    <div class="privacy-panel">
    <div class="section-head section-head-dark">
      <h2 id="boundary-title">Ce qui quitte la machine</h2>
      <p class="section-lead">Mailsense fait quatre types de requêtes réseau. Chacune a une raison visible.</p>
    </div>
      <div class="privacy-grid">
        <div class="boundary-col boundary-leaves">
          <h3>Sortent, uniquement à l&#39;exécution</h3>
          <ol>
            <li>Le moteur local et le modèle de plongements épinglés, téléchargés une fois et vérifiés par SHA-256 avant usage.</li>
            <li>Une requête vers un fournisseur distant que vous avez configuré, pour une réponse Ask ou le triage automatique après une synchronisation. Elle porte la question et les champs du message dont cet appel a besoin.</li>
            <li>Une liste de modèles d&#39;un fournisseur quand vous cliquez sur Récupérer les modèles.</li>
            <li>Une petite requête de test quand vous cliquez sur Tester. Un test est facturable et n&#39;est pas couvert par un budget.</li>
          </ol>
        </div>
        <div class="boundary-col boundary-stays">
          <h3>Ne sortent jamais</h3>
          <ol>
            <li>Le stockage du courrier, les pièces jointes, les index et le profil appris. Ce sont des fichiers SQLite dans le répertoire de données que vous avez choisi.</li>
            <li>Le code de classification, de preuve et de classement. Il s&#39;exécute en Go local, pas comme un appel à un service.</li>
            <li>Aucune télémétrie, aucune analyse, aucun appel de mise à jour et aucun compte chez un fournisseur.</li>
          </ol>
        </div>
      </div>
      <p class="note">Mailsense lit le profil Outlook classique déjà ouvert sous Windows. Il ne demande pas d&#39;identifiants Microsoft. Les conditions d&#39;un fournisseur distant régissent ce qu&#39;il fait d&#39;une requête que vous lui envoyez. Voir <a href="privacy.html">confidentialité</a>.</p>
    </div>
  </div>
</section>

<section class="section section-limits" aria-labelledby="limits-title">
  <div class="shell">
    <div class="section-head">
      <h2 id="limits-title">Ce qu&#39;il ne fait pas encore</h2>
      <p class="section-lead">Ces limites font partie de la version actuelle.</p>
    </div>
    <div class="limits-panel">
      <ol class="limits-list">
        <li>
          <div class="limit-text">
            <h3>Seul Outlook classique sous Windows est une source en direct.</h3>
            <p>Il lit le profil par COM. Le nouvel Outlook, Outlook sur le web, IMAP, POP, l&#39;API Gmail, Microsoft Graph et l&#39;import de fichiers ne sont pas connectés dans cette version.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>L&#39;aperçu Linux est compilé depuis les sources.</h3>
            <p>Il s&#39;ouvre, affiche tous les écrans et explique la source manquante. La boîte reste vide parce qu&#39;il n&#39;y a pas encore de connecteur en direct.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>Il lit et classe. Il n&#39;agit pas sur la boîte.</h3>
            <p>Aucun envoi, aucune suppression, aucun déplacement et aucun changement d&#39;état de lecture. Le connecteur est en lecture seule.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>Le triage a besoin d&#39;un décideur.</h3>
            <p>Sans lui, le triage répond 422 provider_not_configured. La recherche et le classifieur local fonctionnent toujours.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>La base n&#39;est pas chiffrée par l&#39;application.</h3>
            <p>Le compte du système d&#39;exploitation et le chiffrement du disque sont la protection. Les clés de fournisseur sous Windows utilisent DPAPI.</p>
          </div>
        </li>
        <li>
          <div class="limit-text">
            <h3>Il n&#39;y a pas de console SQL ni de paquet de diagnostic.</h3>
            <p>Les journaux sont locaux et lisibles dans l&#39;écran Journaux. Ils masquent les secrets.</p>
          </div>
        </li>
      </ol>
    </div>
  </div>
</section>

<section class="section section-install" aria-labelledby="install-title">
  <div class="shell">
    <div class="install-panel">
    <div class="section-head section-head-dark">
      <h2 id="install-title">Installer</h2>
      <p class="section-lead">La version publiée est un installateur Windows ; les sources se compilent aussi sous Linux en aperçu, avec une boîte vide, et cette compilation n&#39;est pas proposée au téléchargement.</p>
    </div>
      <div class="install-grid">
        <div class="install-file-block">
          <p class="install-label">Installateur Windows</p>
          <pre class="install-file"><code>mailsense-&lt;version&gt;-setup.exe</code></pre>
          <p class="note">La version vient de l&#39;étiquette de version. Téléchargez le fichier depuis la <a href="https://github.com/LLM-4-People/mailsense/releases">page des versions</a>.</p>
          <ul class="install-facts">
            <li>S&#39;installe par utilisateur.</li>
            <li>Ne demande pas de droits administrateur.</li>
            <li>Conserve le répertoire de données à la désinstallation.</li>
          </ul>
        </div>
        <div class="install-action-block">
          <p class="install-caveat">Mailsense a besoin du runtime WebView2, déjà présent sous Windows 11 et Windows 10 à jour. La version n&#39;est pas signée, SmartScreen peut donc demander une confirmation.</p>
          <p class="actions"><a class="button button-light" href="https://github.com/LLM-4-People/mailsense/releases">Télécharger l&#39;installateur</a></p>
          <p><a class="quiet-link quiet-link-light" href="first-run.html">Lire le guide du premier démarrage</a></p>
          <p class="install-note">Le premier démarrage écrit le marqueur d&#39;installation, liste les boîtes Outlook et affiche les plafonds de ressources. Rien n&#39;est lu dans le courrier avant que vous choisissiez une boîte.</p>
        </div>
      </div>
    </div>
  </div>
</section>

<section class="status" aria-labelledby="status-title">
  <div class="shell status-line">
    <h2 id="status-title">État</h2>
    <p>Mailsense est en développement actif. Les interfaces et les schémas stockés peuvent changer entre les versions ; le journal des modifications et les notes de version signalent tout ce qui demande une migration.</p>
  </div>
</section>
