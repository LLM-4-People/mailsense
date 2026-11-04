package evidence

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func init() {
	register(Signal{Name: "categories", Version: 1, Type: TypeSet, Opaque: true, Needs: []string{"categories"}, Group: GroupOwner,
		Doc: "The owner's categories on the message, each as an opaque categorical value (the owner's taxonomy, hashed), sorted. NOT causal.",
		Extract: func(in *Input) Value {
			var out []string
			for _, c := range mail.SplitAddresses(in.Msg.Categories) {
				if o := opaque(c); o != "" && !slices.Contains(out, o) {
					out = append(out, o)
				}
			}
			slices.Sort(out)
			return List(out)
		}})
}
