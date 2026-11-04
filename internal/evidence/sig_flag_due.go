package evidence

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/locale"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

func init() {
	register(Signal{Summary: true, Name: "flag_due_days", Version: 1, Type: TypeInt, Needs: []string{"flags"}, Group: GroupOwner,
		Settings: []string{clock.SettingZone},
		Doc:      "Days from the message's day (owner's zone) to the due date of the owner's flag (a date of the owner's calendar, MS-OXOTASK). NOT causal.",
		Extract: func(in *Input) Value {
			day, ok := in.Day()
			if !ok {
				return NotApplicable(ReasonNoDate)
			}
			due, err := time.Parse(mail.TimeLayout, in.Msg.TaskDueDate)
			if in.Msg.TaskDueDate == "" || err != nil {
				return NotApplicable(ReasonNone)
			}
			return Int(locale.Days(due, day))
		}})
}
