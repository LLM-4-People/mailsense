---
layout: default
title: FAQ
lang: fr
---

# FAQ

<!-- nav:start -->
[Documentation](index.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · **FAQ** · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../faq.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


## Mailsense envoie-t-il mon courrier à un service infonuagique ?

Pas par défaut. La source en direct est le profil Outlook classique de cette
machine Windows, et le stockage, les index et le classifieur sont locaux. Une
requête quitte la machine seulement si vous configurez un fournisseur distant et
qu'un appel s'exécute, ou quand le moteur local télécharge ses fichiers épinglés.
Voir [confidentialité](privacy.md).

## Fonctionne-t-il avec Gmail, IMAP ou Microsoft 365 directement ?

Pas dans cette version. La seule source en direct est Outlook classique sous
Windows par COM. Le nouvel Outlook, Outlook sur le web, IMAP, POP, l'API Gmail et
Microsoft Graph ne sont pas connectés.

## Pourquoi la boîte est-elle vide sous Linux ?

La version Linux est un aperçu. Elle ouvre la fenêtre, affiche les écrans et
explique la source manquante, mais elle n'a pas de connecteur de courrier en
direct. La boîte reste vide par conception.

## Faut-il un modèle pour l'utiliser ?

Le triage a besoin d'un décideur et répond `422 provider_not_configured` sans
lui. La recherche fonctionne sans décideur. Le classifieur local continue
d'apprendre de vos réponses parce qu'il s'exécute dans l'application. La
recherche sémantique a besoin d'un embeddeur, que le moteur local peut fournir.

## Où sont mes données ?

La barre d'état affiche le répertoire de données. Sous Windows, c'est
`%LOCALAPPDATA%\Mailsense` ; sous Linux, c'est
`${XDG_DATA_HOME:-~/.local/share}/mailsense`. Définissez `MAILSENSE_DATA_DIR`
avant le lancement pour en utiliser un autre.

## Comment supprimer mes données ?

Fermez Mailsense et supprimez le répertoire de données. Cela retire le stockage
du courrier, les index, les réglages, le modèle appris et les journaux. La
désinstallation de l'application ne les supprime pas.

## Comment revoir l'écran du premier démarrage ?

Remettez `ui.setup.done` à faux dans les réglages, ou réinitialisez ce réglage.
L'écran s'ouvre au prochain démarrage quand le marqueur est faux et qu'aucune
boîte n'est choisie.

## Pourquoi l'écran de configuration se rouvre-t-il ?

L'écran s'ouvre quand `ui.setup.done` est faux et qu'aucune boîte n'est choisie.
Terminez l'écran, choisissez une boîte dans Sources, ou remettez le marqueur à
vrai.

## Peut-il envoyer, supprimer ou déplacer mes messages ?

Non. Le connecteur publié lit le profil Outlook. Il n'envoie pas, ne supprime
pas, ne déplace pas les messages et ne change pas leur état de lecture.

## Fonctionne-t-il hors ligne ?

Les écrans locaux, la recherche plein texte, le classifieur local et le moteur
local fonctionnent sans réseau une fois le moteur installé. Le téléchargement du
moteur, les tests de fournisseur, les listes de modèles et les appels aux
fournisseurs distants ont besoin d'un réseau.

## Est-il gratuit ?

Le code est publié sous AGPL-3.0-or-later. Ce dépôt n'a ni compte, ni palier
payant, ni service central. Un fournisseur distant que vous configurez vous
facture selon ses propres conditions.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
