---
layout: default
title: Fournisseurs
lang: fr
---

# Fournisseurs

<!-- nav:start -->
[Documentation](index.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · **Fournisseurs** · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../providers.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


Un fournisseur est une fiche que vous ajoutez. L'application n'en fournit aucun
et n'a pas de clé par défaut. Le protocole décide du rôle que la fiche peut
remplir :

| Protocole | Rôle |
| --- | --- |
| `systemone`, `openai_decisions` | Décideur |
| `openai_chat` | Générateur |
| `openai_embeddings` | Embeddeur |

Quatre réglages choisissent les fiches :

- `triage.decider` pour le triage.
- `judge.provider` pour le juge qui propose des étiquettes.
- `qa.provider` pour les réponses rédigées.
- `embed.provider` pour les embeddings sémantiques.

Une fiche utilisée par un rôle ne peut pas être supprimée ni changée de type
tant que le rôle ne pointe pas ailleurs.

## Ajouter un fournisseur

Ouvrez Réglages, Fournisseurs et choisissez Ajouter. Le formulaire demande un
libellé, un protocole, une adresse de base, un modèle et les champs exigés par ce
protocole. Un décideur a aussi besoin de ses budgets de questions et de jetons.
Un générateur a besoin des deux prix et d'un plafond de sortie. Un embeddeur a
besoin d'une spécification d'embedding complète.

L'identifiant de la fiche est un slug en minuscules et ne change plus après la
création. Le fichier des fournisseurs est `providers.json` dans le répertoire de
données.

## Clés

Si vous laissez le nom du secret vide, le formulaire propose
`PROVIDER_<ID>_API_KEY`. La valeur passe par la route des secrets, jamais par la
fiche du fournisseur. Sous Windows, le secret est chiffré avec DPAPI. Sur les
autres plateformes, le magasin de secrets est en lecture seule et l'application
lit `MAILSENSE_SECRET_<NAME>` dans l'environnement.

## Test et liste de modèles

Tester fait une petite requête réelle : une sonde d'une question pour un
décideur, un court appel de conversation pour un générateur, ou un court
embedding. Le résultat indique l'étape qui a échoué. Un test est facturable et
n'est pas couvert par un budget.

Récupérer les modèles envoie `GET {base_url}/models` avec la clé saisie. La liste
sert à appliquer un modèle ; elle n'est pas stockée. Les limites et les prix
manquants restent vides au lieu d'être inventés.

## Sans décideur

Le triage répond `422 provider_not_configured`. Le triage automatique reste
inactif et journalise la raison. La recherche fonctionne toujours. Le classifieur
local continue d'apprendre de vos réponses, parce qu'il s'exécute dans
l'application et n'utilise pas de fournisseur.

Un décideur peut être un service distant ou un point d'accès local compatible
OpenAI que vous exécutez vous-même.

## Ce qui quitte la machine

- La découverte des modèles et le test du fournisseur, quand vous les demandez.
- Les décisions de triage, avec l'expéditeur, l'objet, un aperçu tronqué du
  corps, les destinataires, le dossier, l'historique récent et des exemples
  d'étiquettes.
- Les requêtes du juge, qui envoient le texte des messages au générateur choisi.
- Les réponses rédigées, qui envoient les messages retrouvés que la réponse cite.
- Les embeddings, sauf si `embed.provider` pointe vers le moteur local.

Rien d'autre n'est envoyé. Voir [confidentialité](privacy.md) pour la liste
complète des requêtes.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
