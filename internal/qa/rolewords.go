package qa

// roleMailboxWords is the one vocabulary of shared mailboxes and service
// identities: a local part or display-name token in this set never names a
// person, and it is what a company-contact answer lists as a desk. Two lists
// used to disagree (a display name was judged by one and a question token by
// the other), so "RH" was a role in one path and a person in another; this is
// their union, judged everywhere the same way.
var roleMailboxWords = wordSet("info infos information informations contact contacts support sales admin " +
	"administration team equipe service services reception accueil bureau office hello bonjour " +
	"noreply webmaster postmaster marketing direction secretariat rh hr ressources humaines " +
	"facturation billing accounting comptabilite compta commande commandes orders " +
	"accounts microsoft google apple notifications notification")
