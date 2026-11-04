---
layout: default
title: Demander
lang: fr
---

# Demander

<!-- nav:start -->
<div class="doc-index-wrap">
  <nav class="doc-index" aria-label="Documentation">
  <a href="index.html">Documentation</a>
  <span class="doc-index-current" aria-current="page">Demander</span>
  <a href="install.html">Installation</a>
  <a href="first-run.html">Premier démarrage</a>
  <a href="console.html">Console</a>
  <a href="mailboxes.html">Boîtes</a>
  <a href="providers.html">Fournisseurs</a>
  <a href="learning.html">Apprentissage</a>
  <a href="faq.html">FAQ</a>
  <a href="privacy.html">Confidentialité</a>
  <a href="security.html">Sécurité</a>
  <a href="troubleshooting.html">Dépannage</a>
  <a href="contributing.html">Contribuer</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->

Ask répond à une question sur le courrier de cette machine. Vous posez une
question et la réponse arrive sous forme de décompte calculé, de classement, de
liste de messages correspondants ou de réponse écrite avec citations.

## Ce qu'il répond

- Un décompte ou un classement sur les métadonnées du courrier : combien de
  messages sont urgents, quels expéditeurs ont écrit le plus, quel mois a été le
  plus chargé.
- Une liste de messages correspondants : quels messages parlent de la facture.
- Un fait tiré du courrier : ce que la proposition signée vous demande de
  confirmer.
- Un résumé d'un fil ou d'une journée.

Une question fait entre 3 et 500 caractères. Le champ propose quelques questions
suggérées, et une nouvelle conversation ouvre un fil neuf.

## Les trois modes

Le sélecteur de modèle se trouve à côté du champ de question.

| Mode | Ce qui répond | Quand un modèle est appelé |
| --- | --- | --- |
| Auto | L'index local et le classement quand la question a une réponse exacte, sinon le fournisseur configuré ou les messages correspondants | Seulement pour une question qu'aucune intention exacte ne couvre |
| Local | L'index local, le classement et le profil stocké | Jamais |
| Distant | Le fournisseur configuré, ancré dans les messages récupérés | Pour chaque question avec des preuves récupérées |

Le mode Distant n'est proposé que lorsqu'un fournisseur est configuré et
utilisable. Sinon, le sélecteur dit pourquoi et mène aux réglages du
fournisseur, et Auto et Local restent disponibles.

## Comment une réponse écrite est ancrée

Une réponse écrite n'est produite que par un fournisseur configuré. Le modèle
voit des messages numérotés, récupérés dans l'index local, et marque chaque
affirmation par un numéro de message, par exemple `[1]`. Le serveur vérifie
chaque marqueur contre l'ensemble récupéré.

Une réponse qui cite un numéro hors de cet ensemble est refusée en entier. Le
serveur renumérote ensuite les marqueurs en positions de citation, ajoute une
source que le modèle a nommée dans sa liste sans la citer dans le texte, et ne
garde que les citations qui pointent vers un message réel. La consigne demande
au plus 120 mots.

### Quand l'ancrage échoue

Quand aucune réponse ni citation ne survit, l'écran garde les messages
correspondants et affiche un avis qui nomme la cause : aucun modèle configuré, un
appel en échec ou une réponse qui n'a pas pu être ancrée. L'application ne montre
jamais une affirmation écrite sans sa source en dessous.

## Citations et historique

Chaque ligne de citation porte l'objet, l'expéditeur et la date, et ouvre le
message qu'elle nomme. La même citation apparaît comme pastille numérotée dans le
texte de la réponse.

Chaque réponse est conservée avec sa question, sa route, son mode, ses citations
et, quand un modèle a servi, son fournisseur et son modèle. Le rail d'historique
rouvre une réponse conservée, et Effacer l'historique retire toutes les réponses
conservées.

## Quand il n'y a pas de modèle

Local n'appelle jamais de modèle. Un décompte structuré, un classement et le
profil d'une organisation répondent quand même. Une question libre renvoie les
messages correspondants et un avis qui dit que Local n'appelle jamais de modèle,
avec la possibilité de passer en Auto ou en Distant.

Un appel au fournisseur en échec et une réponse qui n'a pas pu être ancrée
gardent les messages correspondants à l'écran. Un index en préparation ou un
délai de récupération dépassé garde la question et propose Réessayer. Une
question sans correspondance suggère d'autres mots, une question plus courte ou
un sujet plus large.

Le triage a besoin d'un décideur configuré. Ask en mode Local n'en a pas besoin.

## Ce que porte une requête au fournisseur

Quand un fournisseur est utilisé, la requête porte la question, la langue
demandée, la date du jour, votre fuseau horaire, les messages numérotés que la
recherche locale a récupérés, l'état des fils, la ligne du propriétaire, qui vous
nomme avec vos adresses quand la boîte les résout, et les tours précédents de la
même conversation, jusqu'à six.

Chaque message numéroté porte son objet, son sens, le nom et l'adresse de
l'expéditeur, la date de réception, l'état de lecture et d'importance quand il est
connu, la présence de pièces jointes, les lignes À et Cc, les étiquettes apprises,
les signaux de priorité et un extrait du corps d'au plus 600 caractères. Rien
d'autre ne quitte la machine pour cet appel. Les conditions du fournisseur
régissent ce qu'il fait de la requête. Voir [confidentialité](privacy.md).

## Ce qu'il ne fait pas

- Il ne cherche pas sur le web.
- Il n'envoie, ne supprime, ne déplace et ne marque aucun message comme lu. Le
  connecteur est en lecture seule.
- Il n'utilise aucun compte chez un fournisseur.
- Il n'affirme pas un fait absent des messages récupérés.
- Il est borné par `qa.max_messages`, `qa.retrieval_timeout`, `qa.call_timeout`
  et les deux réglages de budget.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Code source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/fr/">Documentation</a> · <a href="privacy.html">Confidentialité</a></p>
<!-- footer:end -->
