---
layout: default
title: "Mailsense (français)"
lang: "fr"
description: "Mailsense transforme votre boîte Outlook en une liste classée de ce qui vous attend, avec la raison sur chaque ligne."
image: "../assets/banner-fr.svg"
---

<header class="hero">
  <div class="hero-copy">
    <p class="eyebrow">Tri du courrier local sous Windows</p>
    <h1>Mailsense</h1>
    <p class="promise">Mailsense transforme votre boîte Outlook en une liste classée de ce qui vous attend, avec la raison sur chaque ligne.</p>
    <p class="lede">Il lit le profil Outlook classique déjà ouvert sur votre PC. Le stockage, le classifieur et le profil appris restent sur votre disque. Un modèle distant ne s&#39;exécute que si vous en ajoutez un.</p>
    <p class="hero-actions"><a class="button" href="install.html">Installer</a><a class="text-link" href="first-run.html">Lire le guide du premier démarrage</a></p>
    <p class="hero-note">Installateur Windows, ou aperçu Linux. Sans compte, sans télémétrie.</p>
  </div>
  <figure class="hero-art">
    <img src="../assets/queue-fr.svg" width="640" height="480" alt="Une file classée inventée de trois lignes : vérifier une facture, répondre à un lancement, lire des notes d&#39;atelier.">
    <figcaption>Une file inventée. L&#39;action et la raison figurent sur chaque ligne.</figcaption>
  </figure>
</header>

[Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · [English](../index.md)

<section class="differences" aria-labelledby="differences-title">
  <h2 id="differences-title">Trois choses qu&#39;il fait autrement</h2>
  <p class="section-lead">Chacune est un mécanisme que vous pouvez inspecter.</p>
  <div class="cards">
    <article class="card">
      <p class="card-tag">01 / Preuve</p>
      <h3>Chaque étiquette nomme sa source.</h3>
      <p>Une décision est stockée comme des réponses à des questions nommées, avec la confiance de chaque réponse quand ce type de question en porte une et les éléments de preuve qui la soutiennent. Le détail du message montre les champs lus par le modèle. Quand un index est partiel, l&#39;écran nomme la partie manquante.</p>
    </article>
    <article class="card">
      <p class="card-tag">02 / File</p>
      <h3>La file est ordonnée par une règle locale fixe.</h3>
      <p>Un moteur Go local ordonne chaque message à partir des signaux stockés : un message suspect ajoute 100, une décision 45, une réponse 30, les niveaux d&#39;urgence et d&#39;échéance ajoutent des poids fixes, et la date de réception ajoute au plus 12. Un score de 20 suffit pour entrer, et un signal de suspicion, de décision, de réponse, d&#39;urgence ou d&#39;échéance entre toujours. Le courrier automatisé de masse n&#39;entre jamais, quelle que soit l&#39;urgence de son texte.</p>
    </article>
    <article class="card">
      <p class="card-tag">03 / Apprentissage</p>
      <h3>Il s&#39;abstient tant que vos propres étiquettes ne l&#39;ont pas certifié.</h3>
      <p>Le classifieur local entraîne une tête par question sur vos corrections. Une question n&#39;est certifiée que lorsque la borne inférieure de Wilson de sa précision répondue atteint 0,99 sur une partition mise de côté, avec au moins 200 étiquettes d&#39;entraînement, 50 étiquettes d&#39;évaluation et une couverture de 0,5. En dessous, il s&#39;abstient, et l&#39;écran Apprentissage nomme l&#39;étape qui bloque.</p>
    </article>
  </div>
</section>

<section class="boundary" aria-labelledby="boundary-title">
  <h2 id="boundary-title">Ce qui quitte la machine</h2>
  <p class="section-lead">Mailsense fait quatre types de requêtes réseau. Chacune a une raison visible.</p>
  <div class="boundary-grid">
    <div class="boundary-col leaves"><h3>Sortent, uniquement à l&#39;exécution</h3><ul>
      <li>Le moteur local et le modèle de plongements épinglés, téléchargés une fois et vérifiés par SHA-256 avant usage.</li>
      <li>Une requête vers un fournisseur distant que vous avez configuré, y compris le triage automatique après une synchronisation. Elle porte les champs du message dont cet appel a besoin.</li>
      <li>Une liste de modèles d&#39;un fournisseur quand vous cliquez sur Récupérer les modèles.</li>
      <li>Une petite requête de test quand vous cliquez sur Tester. Un test est facturable et n&#39;est pas couvert par un budget.</li>
    </ul></div>
    <div class="boundary-col stays"><h3>Ne sortent jamais</h3><ul>
      <li>Le stockage du courrier, les pièces jointes, les index et le profil appris. Ce sont des fichiers SQLite dans le répertoire de données que vous avez choisi.</li>
      <li>Le code de classification, de preuve et de classement. Il s&#39;exécute en Go local, pas comme un appel à un service.</li>
      <li>Aucune télémétrie, aucune analyse, aucun appel de mise à jour et aucun compte chez un fournisseur.</li>
    </ul></div>
  </div>
  <p class="note">Mailsense lit le profil Outlook classique déjà ouvert sous Windows. Il ne demande pas d&#39;identifiants Microsoft. Les conditions d&#39;un fournisseur distant régissent ce qu&#39;il fait d&#39;une requête que vous lui envoyez. Voir <a href="privacy.html">confidentialité</a>.</p>
</section>

<section class="limits" aria-labelledby="limits-title">
  <h2 id="limits-title">Ce qu&#39;il ne fait pas encore</h2>
  <p class="section-lead">Ces limites font partie de la version actuelle.</p>
  <ol class="limit-list">
    <li>
      <h3>Seul Outlook classique sous Windows est une source en direct.</h3>
      <p>Il lit le profil par COM. Le nouvel Outlook, Outlook sur le web, IMAP, POP, l&#39;API Gmail, Microsoft Graph et l&#39;import de fichiers ne sont pas connectés dans cette version.</p>
    </li>
    <li>
      <h3>La version Linux est un aperçu.</h3>
      <p>Elle s&#39;ouvre, affiche tous les écrans et explique la source manquante. La boîte reste vide parce qu&#39;il n&#39;y a pas encore de connecteur en direct.</p>
    </li>
    <li>
      <h3>Il lit et classe. Il n&#39;agit pas sur la boîte.</h3>
      <p>Aucun envoi, aucune suppression, aucun déplacement et aucun changement d&#39;état de lecture. Le connecteur est en lecture seule.</p>
    </li>
    <li>
      <h3>Le triage a besoin d&#39;un décideur.</h3>
      <p>Sans lui, le triage répond 422 provider_not_configured. La recherche et le classifieur local fonctionnent toujours.</p>
    </li>
    <li>
      <h3>La base n&#39;est pas chiffrée par l&#39;application.</h3>
      <p>Le compte du système d&#39;exploitation et le chiffrement du disque sont la protection. Les clés de fournisseur sous Windows utilisent DPAPI.</p>
    </li>
    <li>
      <h3>Il n&#39;y a pas de console SQL ni de paquet de diagnostic.</h3>
      <p>Les journaux sont locaux et lisibles dans l&#39;écran Journaux. Ils masquent les secrets.</p>
    </li>
  </ol>
</section>

<section class="install" id="install" aria-labelledby="install-title">
  <div class="install-copy">
    <h2 id="install-title">Installer</h2>
    <p class="section-lead">Windows est la version en direct prise en charge. Linux est un aperçu.</p>
    <h3>Windows</h3><p>Téléchargez l&#39;installateur par utilisateur depuis la <a href="https://github.com/LLM-4-People/mailsense/releases">page des versions</a>. Il a besoin du runtime Microsoft Edge WebView2, déjà présent sous Windows 11 et Windows 10 à jour. La version n&#39;est pas signée, Windows peut donc demander une confirmation.</p>
    <h3>Linux</h3><p>Téléchargez l&#39;archive amd64, décompressez-la et lancez install.sh pour une entrée de menu, ou lancez directement le binaire.</p>
  </div>
  <div class="install-panel">
    <p class="panel-label">Linux preview</p>
    <pre><code>tar -xzf mailsense-*-linux-amd64.tar.gz &amp;&amp; cd mailsense-*-linux-amd64 &amp;&amp; ./install.sh</code></pre>
    <p class="note">Une commande pour l&#39;aperçu Linux, depuis le répertoire qui contient l&#39;archive téléchargée.</p>
    <a class="button button-light" href="first-run.html">Lire le guide du premier démarrage</a>
  </div>
</section>

<p class="install-note">Le premier démarrage écrit le marqueur d&#39;installation, liste les boîtes Outlook et affiche les plafonds de ressources. Rien n&#39;est lu dans le courrier avant que vous choisissiez une boîte.</p>

<section class="status" aria-labelledby="status-title">
  <h2 id="status-title">État</h2>
  <p>Mailsense est en développement actif. Les interfaces et les schémas stockés peuvent changer entre les versions ; le journal des modifications et les notes de version signalent tout ce qui demande une migration.</p>
</section>

<footer class="page-footer">
  <p><strong>Mailsense</strong> · <a href="https://github.com/LLM-4-People/mailsense">Code source</a> · <a href="https://github.com/LLM-4-People/mailsense/LICENSE">AGPL-3.0-or-later</a> · <a href="https://llm-4-people.github.io/mailsense/fr/">Documentation</a></p>
  <p><a href="privacy.html">Confidentialité</a> · Votre boîte, classée. Chaque ligne s&#39;explique.</p>
</footer>
