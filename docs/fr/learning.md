---
layout: default
title: Apprentissage
lang: fr
---

# Apprentissage

<!-- nav:start -->
[Documentation](index.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · **Apprentissage** · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · [English](../learning.md)
<!-- nav:end -->


L'apprentissage est le seuil qui décide quand Mailsense peut répondre seul. Ce
n'est pas une barre de progression pour elle-même : chaque étape est un fait
dont l'application a besoin avant qu'une réponse certifiée existe.

## Étapes

L'écran Apprentissage montre ces étapes dans l'ordre :

1. **Boîtes.** Une boîte est choisie et possède une identité de propriétaire.
2. **Machine.** Le fuseau horaire est confirmé.
3. **Synchronisation.** Les boîtes choisies sont lues, détails des messages
   compris.
4. **Preuve.** Chaque message a la preuve dont une décision a besoin.
5. **Embeddings.** L'index sémantique couvre le courrier choisi.
6. **Étiquettes.** Assez d'exemples étiquetés existent pour chaque question.
7. **Layer1.** Le classifieur local a une tête entraînée pour la question.
8. **Calibration.** La tête a un seuil qui tient sur des données mises de côté.
9. **Questions.** Au moins une question est certifiée.

Une étape est en attente, en cours, bloquée ou terminée. Une étape bloquée nomme
le réglage ou l'action qui la débloque.

## Quand le seuil s'ouvre

Le seuil s'ouvre quand la sélection actuelle est un sous-ensemble du dernier
périmètre pleinement appris. Le périmètre appris est stocké en local. Retirer une
boîte garde le reste du seuil ouvert. Ajouter une boîte le ferme jusqu'à ce que
la sélection élargie soit apprise.

## Ce que certifié veut dire

La certification est par question. L'application entraîne sur une partition et
teste sur une partition disjointe, divisée par conversation. Une question est
certifiée quand la borne inférieure de Wilson de sa précision répondue atteint
la cible. La cible par défaut est 0,99, la couverture minimale par défaut est
0,5, et les exigences par défaut demandent au moins 200 étiquettes
d'entraînement et 50 étiquettes d'évaluation.

Le seuil s'ouvre dès qu'une question est certifiée. Les autres questions restent
visibles comme avertissements et s'abstiennent au lieu de deviner.

## Mode aperçu

`learning.test_mode` ouvre les écrans avant la certification. C'est un aperçu
réversible : chaque réponse est marquée non certifiée, et le vrai seuil, les
étapes et les étiquettes stockées ne changent pas. Désactivez-le pour revenir au
seuil certifié.

## Ce qui est entraîné et où

Le classifieur local entraîne de petites têtes par question sur des étiquettes
tirées de votre courrier. Les paramètres sont stockés dans la base SQLite
locale. L'entraînement s'exécute dans l'application. Rien de cet entraînement
n'est envoyé à ce projet.

Un fournisseur distant peut quand même recevoir des requêtes si vous en
configurez un : le juge propose des étiquettes, un générateur rédige des
réponses, ou un embeddeur distant remplace le moteur local. Ces requêtes suivent
les conditions du fournisseur.

## Ce que l'écran montre

L'écran Apprentissage montre des comptes, des noms de questions, des clés de
boîtes, l'état des étapes, les blocages et des diagnostics. Il ne montre pas le
contenu des messages. L'accueil indique combien de questions sont certifiées et
renvoie au même écran.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
