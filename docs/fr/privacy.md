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
Le stockage, les index, le classifieur appris et le routage restent sur votre
machine ; la section suivante liste les seules requêtes qui peuvent en sortir.

## Ce qui reste sur votre machine

- Le stockage des messages, les pièces jointes et les index (SQLite).
- Le profil appris, vos corrections et la piste d'audit.
- Les journaux et l'historique, selon votre rétention.
- Le moteur local et son modèle, une fois téléchargés.

## Ce qui sort de la machine

Mailsense fait cinq types de requêtes réseau, et seulement pour les raisons
suivantes :

1. **Téléchargement du moteur local.** Au premier lancement du moteur, ses
   fichiers et son modèle sont téléchargés depuis les adresses épinglées dans
   l'application, puis chaque empreinte SHA-256 est vérifiée.
2. **Requête à un fournisseur.** Seulement par un rôle que vous avez choisi.
   L'application ne livre aucun fournisseur : un rôle sans enregistrement ne
   fait aucun appel et indique ce qui manque. Réglages, Fournisseurs nomme les
   quatre rôles, la fiche de chacun et les champs que chaque appel porte ;
   [Demander](ask.md) liste ce qu'envoie une réponse écrite. Le triage
   automatique et la passe d'indexation qui suit une synchronisation peuvent
   faire un appel sans qu'un écran le demande quand leur rôle est configuré.
   Les conditions du fournisseur s'appliquent.
3. **Liste de modèles d'un fournisseur.** Seulement quand vous demandez à
   l'écran Fournisseurs de récupérer les modèles offerts.
4. **Un test de fournisseur.** Seulement quand vous cliquez sur Tester. Il fait
   un petit appel, qui est facturable et n'est pas couvert par un budget.
5. **Un contrôle des mises à jour.** Après le préchauffage du démarrage, et au
   plus une fois par `update.check.interval` (24 heures par défaut),
   l'application demande à GitHub la dernière version publiée de Mailsense.
   C'est un `GET` non authentifié vers
   `api.github.com/repos/LLM-4-People/mailsense/releases/latest`, avec un
   en-tête `If-None-Match` après la première réponse. La requête ne porte aucun
   courrier, aucune adresse, aucun compte et aucun identifiant d'installation ;
   GitHub voit l'adresse IP de la machine et les en-têtes HTTP standard. Les
   notes de version de la réponse sont conservées localement en texte brut.
   Quand une version plus récente existe, une requête de plus liste les
   étiquettes récentes pour compter le nombre de versions d'avance. Le contrôle
   est actif par défaut (`update.check.enabled`) et peut être désactivé dans les
   réglages ; un contrôle manuel ne part que si vous le demandez.
6. **Rien d'autre.** Aucune télémétrie, aucune analyse, aucune requête cachée.

Les embeddings ont leur propre défaut. Au premier démarrage, l'application
sélectionne l'embeddeur local épinglé, et le moteur local calcule les embeddings
dans un processus sur votre machine : l'objet et le corps de chaque message
indexé, ainsi que le texte de chaque question ou recherche qu'il encode,
restent sur la machine. Si vous pointez plutôt `embed.provider` vers un service
distant, ces champs lui sont envoyés, une fois par message nouvellement indexé
et une fois par recherche. La passe d'indexation suit une synchronisation qui a
stocké du nouveau courrier, par défaut (`index.after_sync`) : un embeddeur
distant est donc appelé sans qu'un écran le demande.

Le classifieur appris, les preuves qu'il lit et le routage s'exécutent dans
l'application. Un fournisseur distant ne garde pas la requête en local ; ses
conditions régissent ce qu'il fait de ce que vous lui envoyez.

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
