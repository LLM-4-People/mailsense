package learning

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
)

// BlockZone is the machine stage's blocker code: time.zone not confirmed.
const BlockZone = "time_zone_unconfirmed"

// MachineStage checks the facts about this machine the user must confirm
// at first run: the owner's time zone (never assumed silently).
func MachineStage(cfg *config.Config, clk clock.Clock) Stage {
	return machineStage{cfg: cfg, clock: clock.Or(clk)}
}

type machineStage struct {
	cfg   *config.Config
	clock clock.Clock
}

func (machineStage) ID() string { return StageMachine }

func (s machineStage) Status(context.Context) (StageStatus, error) {
	out := StageStatus{State: Done, Progress: Progress{Done: 1, Total: 1, Unit: "facts"}}
	if _, err := clock.Zone(s.cfg.String(clock.SettingZone)); err != nil {
		p := clock.Detected(s.clock.Now())
		out.State, out.Progress.Done = Blocked, 0
		out.Blockers = []Blocker{{Code: BlockZone, Detail: err.Error(),
			Actions: []Action{Do(ActionConfirmTimeZone, "iana", p.IANA, "abbrev", p.Abbrev, "offset", p.Offset)}}}
	}
	return out, nil
}

// ConfirmZone is the action that confirms the owner's time zone (an IANA
// name; "" and "Local" are refused): the user's choice, saved to
// settings.json.
func ConfirmZone(cfg *config.Config, name string) error {
	if _, err := clock.Zone(name); err != nil {
		return err
	}
	return cfg.Set(clock.SettingZone, name)
}
