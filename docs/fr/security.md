---
layout: default
title: Sécurité
lang: fr
---

# Sécurité

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
  <span class="doc-index-current" aria-current="page">Sécurité</span>
  <a href="troubleshooting.html">Dépannage</a>
  <a href="contributing.html">Contribuer</a>
  </nav>
  <span class="doc-index-hint" aria-hidden="true"></span>
  </div>
<!-- nav:end -->


Mailsense est une application de bureau locale. Elle lit un profil de courrier
sur la même machine, sert sa propre interface sur la boucle locale et garde ses
données dans un répertoire que vous contrôlez. Cette page dit ce que ce modèle
protège et ce qu'il ne protège pas.

## Le serveur local

L'API écoute sur `tcp4 127.0.0.1` avec un port éphémère. Le serveur refuse de
démarrer si l'adresse liée n'est pas la boucle locale IPv4 : une autre machine du
réseau ne peut pas l'atteindre.

La fenêtre de bureau reçoit un jeton de lancement dans le fragment de l'URL. Le
script copie le jeton dans `sessionStorage` et le retire de l'adresse avant le
premier appel à l'API. Le jeton n'est écrit ni dans le DOM, ni dans la console,
ni dans un message d'erreur. Chaque requête de l'API le porte. Un processus local
qui peut lire votre stockage de session ou la mémoire du processus est hors de
cette frontière.

## Données au repos

- Le stockage du courrier, les index et le modèle appris forment une base SQLite
  locale. L'application ne les chiffre pas. Le compte du système d'exploitation
  et le chiffrement du disque sont la protection.
- Les clés de fournisseur sous Windows sont chiffrées avec DPAPI. Sur les autres
  plateformes, le magasin de secrets est en lecture seule et lit
  `MAILSENSE_SECRET_<NAME>` dans l'environnement.
- Les journaux sont locaux. Ils masquent les secrets, et le propriétaire des
  journaux applique la rétention que vous avez choisie.

## Intégrité du moteur

Le moteur local est épinglé à une version fixe et à un modèle fixe. Les
téléchargements reprennent par requête de plage et sont vérifiés par taille et
par SHA-256. Le déballage écrit un manifeste avec l'empreinte de chaque fichier
et promeut le répertoire de façon atomique. Avant chaque lancement, l'application
vérifie le modèle, le manifeste, l'exécutable et chaque fichier listé, et elle
refuse tout fichier ou lien non listé.

## Intégrité des versions

Un fichier publié est construit par le workflow de version à partir du commit
étiqueté sur `main`. GitHub conserve une attestation de provenance signée pour
chaque fichier listé dans `SHA256SUMS`, et
`gh attestation verify <file> --repo LLM-4-People/mailsense` la vérifie. La
version publie aussi un SBOM CycloneDX.

Les binaires ne sont pas signés. Windows peut vous demander de confirmer
l'éditeur. L'attestation prouve quel workflow a construit le fichier ; ce n'est
pas un certificat d'éditeur.

## Requêtes réseau

L'application fait ces requêtes, et aucune autre :

1. Le téléchargement des fichiers épinglés du moteur et du modèle.
2. Un appel à un fournisseur distant que vous avez configuré, y compris un
   triage ou une passe d'indexation automatique après une synchronisation quand
   ce rôle pointe vers un service distant.
3. Une liste de modèles d'un fournisseur, quand vous la demandez.
4. Un test de fournisseur, quand vous cliquez sur Tester.

Il n'y a ni télémétrie, ni analyse, ni appel de mise à jour, ni compte central.
[Confidentialité](privacy.md) liste les champs que chaque requête peut porter.

## Le rendu du courrier non fiable

Le corps d'un message est du HTML non fiable. Mailsense le reconstruit au lieu
de lui faire confiance :

- Le serveur local analyse le corps stocké et ne garde qu'une liste fermée
  d'éléments de texte, de listes, de citations, de tableaux et de liens. Les
  scripts, les cadres, les objets, les formulaires et les médias sont retirés
  avec leur contenu.
- Le fragment reconstruit et la feuille de présentation compilée de l'expéditeur
  sont rendus dans un iframe dont le bac à sable n'accorde ni script, ni fenêtre
  surgissante, ni navigation de la page de niveau supérieur, et le document
  porte sa propre CSP : `script-src 'none'`, `form-action 'none'`,
  `base-uri 'none'`.
- Les images distantes deviennent un espace réservé, et un pixel de suivi 1x1
  est supprimé entièrement. Les images en ligne sont récupérées par l'enveloppe
  avec le jeton porteur et remplacées dans le cadre par des URL blob éphémères.
- Un lien n'est conservé que pour http, https et mailto. Un clic ne navigue
  jamais : il ouvre une boîte qui montre la destination exacte et propose de la
  copier.
- Une lecture de pièce jointe est servie depuis des lignes adressées par contenu
  et uniquement comme image matricielle en ligne, avec `nosniff` et une CSP
  d'actif, et seulement si le message passe le contrôle du propriétaire et des
  boîtes choisies.

## Quand un modèle rédige une réponse

La page Demander peut envoyer des messages retrouvés au fournisseur que vous
avez configuré et afficher la réponse qu'il rédige. L'auteur d'un message
contrôle son texte, donc le texte d'un message peut tenter d'orienter cette
réponse. C'est l'injection de prompt indirecte, qu'aucune consigne donnée au
modèle ne peut supprimer. Ce que l'application encadre :

- Les marqueurs de citation sont vérifiés, pas crus sur parole. Une réponse dont
  un marqueur sort du contexte numéroté est refusée en entier ; un marqueur
  valide est renuméroté vers le message qu'il nomme, et la liste des citations
  est construite à partir de ces messages. Cette vérification prouve que le
  message cité existe dans le contexte. Elle ne prouve pas que les mots de la
  réponse en découlent : une réponse orientée peut porter une vraie citation.
- La sortie du modèle ne change aucun état de l'application. Un plan est du
  JSON strict, validé contre des formes fixes et exécuté comme une requête
  locale en lecture seule. Les nombres et les classements sont calculés par du
  code local, jamais repris du modèle. Une réponse ne déclenche ni nouvelle
  recherche, ni requête réseau, ni action sur votre courrier.
- Le modèle ne voit que des données visibles par le propriétaire pour ce tour :
  les messages numérotés retrouvés avec leurs extraits, les comptes et les faits
  calculés localement, et, dans un fil, les tours précédents. Le pied de la
  réponse nomme le fournisseur et le modèle, et chaque citation ouvre le message
  qu'elle nomme. Traitez une réponse rédigée comme une suggestion, pas comme une
  affirmation fiable.

## Signaler une vulnérabilité

Ouvrez un avis de sécurité privé depuis l'onglet Security du dépôt. Indiquez la
version, le système d'exploitation, la route ou l'écran touché, et une
reproduction minimale. Si l'avis privé n'est pas disponible, ouvrez une issue
publique qui décrit l'impact sans détails d'exploitation, et demandez un canal
privé.

Les versions prises en charge sont la dernière version publiée et la branche
`main` actuelle. N'incluez pas de courrier réel, de clé de fournisseur ni de
donnée personnelle dans un signalement.

<!-- footer:start -->
<p class="doc-meta"><a href="https://github.com/LLM-4-People/mailsense">Code source</a> · AGPL-3.0-or-later · <a href="https://llm-4-people.github.io/mailsense/fr/">Documentation</a> · <a href="privacy.html">Confidentialité</a></p>
<!-- footer:end -->
