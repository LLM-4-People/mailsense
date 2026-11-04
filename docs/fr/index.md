---
layout: default
title: "Mailsense (français)"
lang: "fr"
language_switch: true
description: "Posez une question à votre courrier. Il répond par un décompte, une liste ou une réponse écrite qui cite les messages utilisés."
image:
  path: "/assets/social-preview.png"
  width: 1200
  height: 630
  alt: "Mailsense: Demandez à votre courrier. Chaque citation ouvre le message."
---

<header class="hero">
  <div class="hero-copy">
    <p class="eyebrow">Ask et tri local sous Windows</p>
    <h1>Mailsense</h1>
    <p class="promise">Posez une question à votre courrier. Il répond par un décompte, une liste ou une réponse écrite qui cite les messages utilisés.</p>
    <p class="lede">Il lit le profil Outlook classique déjà ouvert sur votre PC. Ask répond depuis l&#39;index local ; une réponse écrite ne vient que d&#39;un modèle que vous avez configuré et des messages qu&#39;il peut citer. Le stockage, le classifieur et le profil appris restent sur votre disque.</p>
    <p class="hero-actions"><a class="button" href="install.html">Installer</a><a class="text-link" href="first-run.html">Lire le guide du premier démarrage</a></p>
    <p class="hero-note">Installateur Windows, ou aperçu Linux. Sans compte, sans télémétrie.</p>
  </div>
  <figure class="hero-art">
    <img src="../assets/queue-fr.svg" width="640" height="480" alt="Une file classée inventée de trois lignes : vérifier une facture, répondre à un lancement, lire des notes d&#39;atelier.">
    <figcaption>La file est la seconde preuve. Une file inventée, avec l&#39;action et la raison sur chaque ligne.</figcaption>
  </figure>
</header>

[Demander](ask.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../" hreflang="en" lang="en" data-lang-switch="en">English</a>

<section class="ask" aria-labelledby="ask-title">
  <h2 id="ask-title">Commencez par une question</h2>
  <p class="section-lead">Ask répond en clair à une question sur votre courrier. L&#39;écran nomme le mécanisme qui a répondu : une requête typée, une recherche ou un modèle que vous avez configuré.</p>
  <ol class="ask-list">
    <li>
      <p class="card-tag">Ancrage</p>
      <h3>Une réponse écrite cite chaque message utilisé.</h3>
      <p>Le modèle doit écrire au plus 120 mots et marque chaque affirmation par un numéro de message. Le serveur vérifie chaque marqueur contre l&#39;ensemble récupéré, refuse une réponse dont un marqueur pointe hors de cet ensemble, renumérote les marqueurs en positions de citation et ne garde une citation que si elle nomme un message réel. Quand aucune citation ne survit, les messages correspondants restent à l&#39;écran avec un avis qui nomme la cause.</p>
    </li>
    <li>
      <p class="card-tag">Local</p>
      <h3>Un décompte ou un classement est calculé, pas rédigé.</h3>
      <p>Un décompte, un classement et le profil d&#39;une organisation sont lus dans l&#39;index local et les faits stockés. Le nombre et les lignes viennent d&#39;une requête typée, jamais d&#39;un texte de modèle. Le mode Local n&#39;appelle jamais de modèle : le même décompte répond quand même, et une question libre renvoie les messages correspondants avec un avis qui dit que Local n&#39;appelle jamais de modèle.</p>
    </li>
    <li>
      <p class="card-tag">Trace</p>
      <h3>Une citation ouvre le message, et la réponse est conservée.</h3>
      <p>Chaque ligne de citation porte l&#39;objet, l&#39;expéditeur et la date, et mène au message qu&#39;elle nomme. Chaque réponse est conservée avec sa question, sa route, son mode, ses citations et, quand un modèle a servi, son fournisseur et son modèle. Le rail d&#39;historique la rouvre, et Effacer l&#39;historique retire toutes les réponses conservées.</p>
    </li>
  </ol>
  <p class="note">Ask est borné par <code>qa.max_messages</code>, <code>qa.retrieval_timeout</code>, <code>qa.call_timeout</code> et les deux réglages de budget. Un index en préparation garde la question et propose Réessayer.</p>
  <p><a class="text-link" href="ask.html">Lire le guide Ask</a></p>
</section>

<section class="differences" aria-labelledby="differences-title">
  <h2 id="differences-title">Trois choses qu&#39;il fait autrement</h2>
  <p class="section-lead">La file, la preuve et le verrou. Chacune est un mécanisme que vous pouvez inspecter.</p>
  <div class="cards">
    <article class="card">
      <p class="card-tag">01 / File</p>
      <h3>La file est ordonnée par une règle locale fixe.</h3>
      <p>Un moteur Go local ordonne chaque message à partir des signaux stockés : un message suspect ajoute 100, une décision 45, une réponse 30, les niveaux d&#39;urgence et d&#39;échéance ajoutent des poids fixes, et la date de réception ajoute au plus 12. Un score de 20 suffit pour entrer, et un signal de suspicion, de décision, de réponse, d&#39;urgence ou d&#39;échéance entre toujours. Le courrier automatisé de masse n&#39;entre jamais, quelle que soit l&#39;urgence de son texte.</p>
    </article>
    <article class="card">
      <p class="card-tag">02 / Preuve</p>
      <h3>Chaque étiquette nomme sa source.</h3>
      <p>Une décision est stockée comme des réponses à des questions nommées, avec la confiance de chaque réponse quand ce type de question en porte une et les éléments de preuve qui la soutiennent. Le détail du message montre les champs lus par le modèle. Quand un index est partiel, l&#39;écran nomme la partie manquante.</p>
    </article>
    <article class="card">
      <p class="card-tag">03 / Apprentissage</p>
      <h3>Il s&#39;abstient tant que vos propres étiquettes ne l&#39;ont pas certifié.</h3>
      <p>Le classifieur local entraîne une tête par question sur vos corrections. Une question n&#39;est certifiée que lorsque la borne inférieure de Wilson de sa précision répondue atteint 0,99 sur une partition mise de côté, avec au moins 200 étiquettes d&#39;entraînement, 50 étiquettes d&#39;évaluation et une couverture de 0,5. En dessous, il s&#39;abstient, et l&#39;écran Apprentissage nomme l&#39;étape qui bloque.</p>
    </article>
  </div>
</section>

<section class="shots" aria-labelledby="shots-title">
  <h2 id="shots-title">Les écrans, à commencer par Ask</h2>
  <p class="section-lead">Capturés depuis l&#39;aperçu Linux avec une boîte inventée. Aucun courrier réel n&#39;apparaît ici.</p>
  <div class="shot-grid">
    <figure class="shot">
      <img src="../assets/screenshots/fr/ask.png" width="1440" height="900" loading="eager" decoding="async" alt="Mailsense: Demander avec une réponse">
      <figcaption><strong>Demander avec une réponse</strong> Ask mène : une question et sa réponse écrite, avec chaque citation sous le texte qui ouvre le message qu&#39;elle nomme.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/home.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Accueil">
      <figcaption><strong>Accueil</strong> La journée en trois colonnes : ce qui attend une réponse, ce qui attend une décision et ce qui arrive.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/mail.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Courrier avec un message ouvert">
      <figcaption><strong>Courrier avec un message ouvert</strong> Le message choisi montre son expéditeur, son corps et les champs lus par le modèle local.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/teach.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Enseigner">
      <figcaption><strong>Enseigner</strong> Une question à la fois. La suggestion est affichée, et votre réponse est l&#39;étiquette qui entraîne le modèle local.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/learning.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Apprentissage">
      <figcaption><strong>Apprentissage</strong> Chaque étape indique sa progression, et l&#39;écran nomme l&#39;étape qui bloque encore la certification.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/sources.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Sources">
      <figcaption><strong>Sources</strong> Le stockage Outlook, la boîte choisie et les dossiers que Mailsense peut lire.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/settings.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Réglages">
      <figcaption><strong>Réglages</strong> Les réglages du quotidien d&#39;abord. Le schéma complet reste à un clic.</figcaption>
    </figure>
    <figure class="shot">
      <img src="../assets/screenshots/fr/first-run.png" width="1440" height="900" loading="lazy" decoding="async" alt="Mailsense: Premier démarrage">
      <figcaption><strong>Premier démarrage</strong> Le premier lancement écrit le marqueur d&#39;installation, nomme le répertoire de données et demande un fuseau horaire avant de lire le courrier.</figcaption>
    </figure>
  </div>
</section>

<section class="boundary" aria-labelledby="boundary-title">
  <h2 id="boundary-title">Ce qui quitte la machine</h2>
  <p class="section-lead">Mailsense fait quatre types de requêtes réseau. Chacune a une raison visible.</p>
  <div class="boundary-grid">
    <div class="boundary-col leaves"><h3>Sortent, uniquement à l&#39;exécution</h3><ul>
      <li>Le moteur local et le modèle de plongements épinglés, téléchargés une fois et vérifiés par SHA-256 avant usage.</li>
      <li>Une requête vers un fournisseur distant que vous avez configuré, pour une réponse Ask ou le triage automatique après une synchronisation. Elle porte la question et les champs du message dont cet appel a besoin.</li>
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
  <p><a href="privacy.html">Confidentialité</a> · Demandez à votre courrier. Chaque citation ouvre le message.</p>
</footer>
