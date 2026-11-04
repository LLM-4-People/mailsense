---
layout: default
title: Mailsense (français)
lang: fr
---

# Mailsense

**Votre boîte mail, triée. Chaque décision expliquée. Votre courrier reste sur
votre machine ; l'analyse locale s'y exécute ; un modèle distant n'est utilisé
que si vous en configurez un.**

Mailsense lit le courrier déjà présent dans votre profil Outlook classique sous
Windows, indique ce que chaque message est, ce qu'il demande, et ce qui mérite
votre attention en premier. Il montre la raison de chaque verdict, apprend de
vos corrections et conserve tout le dossier dans une base locale que vous pouvez
inspecter. La version Linux est un aperçu sans source de courrier en direct.

[English](../) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md)

![Mailsense](../assets/banner-fr.svg)

## Ce qu'il fait

- **Un tri que vous pouvez vérifier.** Catégorie, sujet, demande, obligation de
  réponse, urgence, échéance, sentiment, suspicion d'hameçonnage, et la preuve
  de chacun.
- **Une boîte classée.** Répondre, décider, payer, planifier ou lire, dans
  l'ordre de ce que le message exige vraiment, avec la raison sur la ligne.
- **Une boîte qui s'améliore.** Vos corrections deviennent des exemples ; votre
  façon de répondre devient un priori.
- **La vérité des fils.** Qui a parlé en dernier, si vous devez une réponse, et
  quel fil reste ouvert.
- **Entreprises et personnes.** Les contacts déclarés fusionnés avec ce que le
  courrier a observé.
- **Des questions en langage courant.** Comptages et listes structurés d'abord ;
  un résumé rédigé par un modèle seulement si vous en avez configuré un,
  toujours avec des citations.
- **Local par défaut.** SQLite sur disque, embeddings locaux téléchargés une
  fois, plafonds CPU, mémoire et disque configurables, et travail de fond mis en
  pause au plafond au lieu de lutter contre la machine.

## Pourquoi c'est différent

La plupart des assistants cachent le raisonnement. Mailsense traite chaque
étiquette comme une affirmation avec une source : le profil qui l'a produite,
les champs lus, et la confiance associée. Quand l'indexation n'est pas terminée,
il le dit au lieu de faire croire que la réponse est complète.

## Ce qui s'exécute où

| Partie | Défaut | Option |
| --- | --- | --- |
| Source de courrier en direct | Outlook classique sous Windows | Aucune sous Linux pour l'instant |
| Stockage et index du courrier | SQLite local | Un répertoire de données choisi |
| Embeddings sémantiques | Téléchargé au premier usage (environ 342 Mo sous Windows, 328 Mo sous Linux) | Un fournisseur d'embeddings distant configuré |
| Classification des messages | Classifieur Go local ; le triage exige un décideur | Le décideur choisi, y compris un point d'accès local compatible OpenAI |
| Réponses rédigées | Désactivées par défaut | Le fournisseur de rédaction choisi |

Mailsense lit le profil Outlook classique déjà ouvert sous Windows ; il ne
demande pas d'identifiants Microsoft. Un fournisseur distant ne reçoit que les
champs nécessaires à une requête, et seulement quand elle s'exécute. Voir
[confidentialité](privacy.md).

## Démarrer

1. [Installer Mailsense](install.md) sous Windows. La version Linux est un
   aperçu sans source de courrier en direct.
2. Suivre le [premier démarrage](first-run.md) : un parcours simple, et un
   parcours avancé pour les fournisseurs, le stockage et les ressources.
3. Lire le [modèle de confidentialité](privacy.md) en une page.

## État

Mailsense est en développement actif. Les interfaces et les schémas stockés
peuvent changer entre les versions ; le journal des modifications et les notes
de version signalent tout ce qui demande une migration.

[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later
