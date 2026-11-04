---
layout: default
title: Boîtes
lang: fr
---

# Boîtes

<!-- nav:start -->
[Documentation](index.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · **Boîtes** · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../mailboxes.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


Mailsense lit le courrier en direct depuis le profil Outlook classique déjà
ouvert sur une machine Windows. Il ne demande pas d'identifiants Microsoft et ne
se connecte pas à un service de messagerie. La version Linux n'a pas encore de
source en direct.

## Ce que la version publiée peut lire

| Source | État |
| --- | --- |
| Outlook classique sous Windows (COM) | Offerte |
| Nouvel Outlook, Outlook sur le web | Non offert |
| IMAP, POP, API Gmail, Microsoft Graph | Absents de cette version |
| Fichiers mbox, maildir, .eml | Non exposés dans cette version |

Les lecteurs de fichiers et l'importateur d'archives existent dans le code pour
les tests et de futurs portages, mais l'application ne les enregistre pas comme
sources.

## Découverte

Ouvrez Sources et choisissez Chercher les boîtes. La découverte liste les boîtes
du profil Outlook et leurs dossiers. Elle ne lit aucun message. Une boîte
affiche un nombre de messages et de dossiers seulement après votre choix, parce
que compter une boîte non choisie exigerait de la lire.

La découverte est aussi ce qui rend une boîte sélectionnable. Une clé qu'aucune
découverte n'a vue ne peut pas être choisie.

## Choisir une boîte

Choisissez une ou plusieurs boîtes. Rien n'est lu avant ce choix. La première
synchronisation tourne en arrière-plan et la barre des tâches montre son
avancement.

- Choisie signifie que la boîte est dans `mailboxes.selected` et que ses dossiers
  sont visibles dans tous les écrans.
- Découverte signifie que la dernière découverte a écrit une ligne pour la
  boîte. Une clé choisie sans ligne garde sa clé et reste marquée comme non
  découverte.
- Retirer une boîte conserve le courrier stocké, mais le retire de tous les
  écrans et du périmètre d'apprentissage. La base n'est pas supprimée.

Si la boîte choisie n'a pas d'adresse de propriétaire, l'apprentissage s'arrête
avec `no_owner_identity` ou `owner_never_reported` et propose Déclarer une
adresse. Cette adresse indique à Mailsense quels messages vous avez envoyés, ce
dont dépend l'obligation de réponse.

## Ce qu'une synchronisation lit

- L'inventaire des dossiers et les résumés des messages.
- Les corps complets, les en-têtes et les destinataires à l'étape de détail.
- Les occurrences de calendrier et les carnets d'adresses quand ces réglages
  sont activés.

Outlook écrit d'abord une ligne de résumé, puis la complète. L'interface signale
un index partiel au lieu de faire croire que la boîte est vide. La recherche
sémantique saute un message tant que son détail n'est pas complet ; la recherche
plein texte peut déjà le trouver.

## Courrier retiré

Quand un dossier perd au moins 100 messages ou plus de la moitié de ses
messages, Mailsense retient le retrait au lieu de l'appliquer. Confirmez-le dans
Sources, ou laissez-le s'appliquer après 72 heures.

## Linux

Il n'y a pas de source de courrier en direct sous Linux dans cette version. La
fenêtre s'ouvre, les écrans s'affichent, et Sources explique que la boîte reste
vide. La source de fichiers n'est pas exposée.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
