---
layout: default
title: Dépannage
lang: fr
---

# Dépannage

<!-- nav:start -->
[Documentation](index.md) · [Demander](ask.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · **Dépannage** · [Contribuer](contributing.md) · <a href="../troubleshooting.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


## La boîte est vide

Ouvrez Sources. Lancez Découvrir, choisissez la boîte que Mailsense peut lire,
puis démarrez une synchronisation. La barre des tâches indique les messages
analysés et le total. Un écran qui dépend d'un index partiel le dit ; il ne fait
pas croire que la boîte est vide.

Sous Linux, il n'y a pas encore de source de courrier en direct : la boîte reste
vide par conception.

## « L'index de recherche se prépare »

La partie sémantique de l'index n'est pas complète. Les résultats plein texte
restent disponibles. L'avis sur Recherche ou Demander indique la partie
manquante. Ce n'est pas une réponse « aucun résultat ».

## L'application met le travail de fond en pause

Les plafonds de ressources font leur travail. Ouvrez Réglages, Général, et
augmentez le plafond CPU, mémoire ou disque, ou attendez que la machine se calme.
L'écran Journaux enregistre la pause avec la ressource et la limite déclenchée.

## Un appel de modèle échoue

- Vérifiez la fiche du fournisseur et l'adresse dans Réglages, Fournisseurs.
- Lancez le test du fournisseur ; il indique l'étape en échec.
- Un délai dépassé n'est pas une mauvaise réponse. Réessayez, ou réduisez la
  taille de la requête.
- Si aucun décideur n'est configuré, le triage répond
  `422 provider_not_configured`. Ajoutez un fournisseur et choisissez-le comme
  décideur. La recherche fonctionne toujours.

## Outlook est introuvable

Mailsense lit le profil Outlook classique déjà ouvert sous Windows. Il ne se
connecte pas à Microsoft. Vérifiez qu'Outlook classique est installé, ouvert et
qu'il contient au moins une boîte. Sous Linux, le courrier en direct n'est pas
encore offert.

## L'installation se rouvre

Le marqueur d'installation est le réglage `ui.setup.done`. S'il est faux et
qu'aucune boîte n'est choisie, Mailsense rouvre l'installation au prochain
démarrage. Remettez-le à vrai dans les réglages, ou choisissez une boîte dans
Sources.

## Où sont les journaux

Ouvrez Journaux pour les tâches, avertissements et erreurs. Le répertoire de
données contient aussi les fichiers de journal. Il n'y a pas de paquet de
diagnostic séparé.

## Où sont les données

La barre d'état affiche le répertoire de données. C'est
`%LOCALAPPDATA%\Mailsense` sous Windows et
`${XDG_DATA_HOME:-~/.local/share}/mailsense` sous Linux. Définissez
`MAILSENSE_DATA_DIR` avant le lancement pour en utiliser un autre.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
