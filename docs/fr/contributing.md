---
layout: default
title: Contribuer
lang: fr
---

# Contribuer

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
  <a href="privacy.html">Confidentialité</a>
  <a href="security.html">Sécurité</a>
  <a href="troubleshooting.html">Dépannage</a>
  <span class="doc-index-current" aria-current="page">Contribuer</span>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->


Mailsense est écrit en Go pour l'application et en JavaScript simple pour
l'interface. Il n'y a pas de cadre de travail de chaque côté. Le dépôt est la
source de vérité pour la compilation, les tests et les pages publiques.

## Commencer ici

[CONTRIBUTING.md](https://github.com/LLM-4-People/mailsense/blob/main/CONTRIBUTING.md)
est le contrat de contribution. Il possède la chaîne d'outils, la commande
exacte de chaque contrôle, les règles des pages publiques, les règles de
l'interface, le format des commits et l'exigence de signature. Cette page est le
résumé public.

La participation est couverte par le
[code de conduite](https://github.com/LLM-4-People/mailsense/blob/main/CODE_OF_CONDUCT.md).

## Flux de travail

Ouvrez une demande de tirage vers `main`. Un changement demande un test, une note
dans la demande qui explique comment il a été vérifié, et une ligne
`Signed-off-by` sur chaque commit. L'éditeur web de GitHub ajoute la signature ;
localement, utilisez `git commit -s`.

Les contrôles de la demande exécutent les mêmes portes qu'une version, et le
mainteneur relit le résultat avant la fusion.

## Signaler un bogue

Indiquez la version, le système d'exploitation, l'écran ou la route, ce que vous
attendiez, ce qui est arrivé, et l'entrée pertinente des journaux. Retirez le
contenu du courrier, les adresses et les clés de fournisseur avant de coller un
journal.

Les signalements de sécurité passent par le canal privé de la
[page sécurité](security.md).

## Licence

Le projet est publié sous AGPL-3.0-or-later. En contribuant, vous acceptez que
votre contribution soit distribuée sous la même licence. Il n'y a pas d'accord
de licence de contributeur séparé.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Code source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/fr/">Documentation</a> · <a href="privacy.html">Confidentialité</a></p>
<!-- footer:end -->
