---
layout: default
title: Confidentialité
lang: fr
---

# Confidentialité

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <a href="ask.html">Demander</a>
  <a href="install.html">Installation</a>
  <a href="first-run.html">Premier démarrage</a>
  <a href="console.html">Console</a>
  <a href="mailboxes.html">Boîtes</a>
  <a href="providers.html">Fournisseurs</a>
  <a href="learning.html">Apprentissage</a>
  <a href="faq.html">FAQ</a>
  <span class="doc-index-current" aria-current="page">Confidentialité</span>
  <a href="security.html">Sécurité</a>
  <a href="troubleshooting.html">Dépannage</a>
  <a href="contributing.html">Contribuer</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
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
   fournisseur distant et qu'un appel s'exécute. La requête contient la question
   quand Ask rédige une réponse, et les champs du message dont cet appel a
   besoin. Les conditions du fournisseur s'appliquent. Réglages, Fournisseurs
   indique l'adresse et le modèle. Le triage automatique peut faire cet appel
   après une synchronisation sans qu'un écran le demande ; le test d'un
   fournisseur fait un petit appel quand vous cliquez sur Tester.
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
Sous Windows, la désinstallation conserve le répertoire jusqu'à ce que vous le
supprimiez.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Code source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/fr/">Documentation</a> · <a href="privacy.html">Confidentialité</a></p>
<!-- footer:end -->
