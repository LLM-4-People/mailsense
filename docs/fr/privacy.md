---
layout: default
title: Confidentialité
lang: fr
---

# Confidentialité

<!-- nav:start -->
[Documentation](index.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · **Confidentialité** · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../privacy.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


Mailsense fonctionne d'abord en local. La source en direct est le profil Outlook
classique déjà ouvert sur votre machine Windows. Mailsense ne demande pas
d'identifiants Microsoft, ne crée pas de compte et n'a pas de service central.
Le moteur local calcule les embeddings sur votre machine ; la classification et
le routage sont du code Go local, pas un service infonuagique.

## Ce qui reste sur votre machine

- Le stockage des messages, les pièces jointes et les index (SQLite).
- Le profil appris, vos corrections et la piste d'audit.
- Les journaux et l'historique, selon votre rétention.
- Le moteur local et son modèle, une fois téléchargés.

## Ce qui sort de la machine

Mailsense fait quatre types de requêtes réseau, et seulement pour les raisons
suivantes :

1. **Téléchargement du moteur local.** Au premier lancement du moteur, ses
   fichiers et son modèle sont téléchargés depuis les adresses épinglées dans
   l'application, puis chaque empreinte SHA-256 est vérifiée.
2. **Requête à un fournisseur distant.** Seulement si vous configurez un
   fournisseur distant et qu'un appel s'exécute. La requête contient les champs
   du message dont cet appel a besoin. Les conditions du fournisseur
   s'appliquent. Réglages, Fournisseurs indique l'adresse et le modèle. Le
   triage automatique peut faire cet appel après une synchronisation sans qu'un
   écran le demande ; le test d'un fournisseur fait un petit appel quand vous
   cliquez sur Tester.
3. **Liste de modèles d'un fournisseur.** Seulement quand vous demandez à
   l'écran Fournisseurs de récupérer les modèles offerts.
4. **Rien d'autre.** Aucune télémétrie, aucune analyse, aucun appel de mise à
   jour, aucune requête cachée.

Le moteur local calcule les embeddings sur votre machine. La classification, la
preuve et le routage sont du code Go local. Un fournisseur distant ne garde pas
la requête en local ; ses conditions régissent ce qu'il fait de ce que vous lui
envoyez.

## Apprentissage

Le classifieur local entraîne de petites têtes par question sur des étiquettes
tirées de votre courrier, et les stocke dans la base locale. L'entraînement se
fait sur votre disque. Rien de cet entraînement n'est envoyé à ce projet. Si
vous configurez un fournisseur distant, ses conditions régissent les requêtes
que vous lui envoyez.

## Vérifier vous-même

Chaque décision stockée porte sa source, les champs du message qu'elle a lus et
une confiance. Le détail du message montre cette preuve ; l'écran Journaux montre
les tâches, avertissements et erreurs derrière un écran. Il n'y a pas de second
registre caché.

## Supprimer vos données

Le répertoire de données est affiché dans la barre d'état de l'application.
Supprimez-le pour retirer le stockage, les index, les réglages et les journaux.
Sous Windows, la désinstallation conserve le répertoire ; sous Linux, supprimer
le binaire le conserve aussi.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
