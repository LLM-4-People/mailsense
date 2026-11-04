---
layout: default
title: Sécurité
lang: fr
---

# Sécurité

[English](../security.md)

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

La version publiée n'est pas encore signée. Windows peut vous demander de
confirmer l'éditeur. L'intégrité vient des empreintes épinglées, pas d'une
signature.

## Requêtes réseau

L'application fait ces requêtes, et aucune autre :

1. Le téléchargement des fichiers épinglés du moteur et du modèle.
2. Un appel à un fournisseur distant que vous avez configuré, y compris le
   triage automatique après une synchronisation.
3. Une liste de modèles d'un fournisseur, quand vous la demandez.
4. Un test de fournisseur, quand vous cliquez sur Tester.

Il n'y a ni télémétrie, ni analyse, ni appel de mise à jour, ni compte central.
[Confidentialité](privacy.md) liste les champs que chaque requête peut porter.

## Signaler une vulnérabilité

Ouvrez un avis de sécurité privé depuis l'onglet Security du dépôt. Indiquez la
version, le système d'exploitation, la route ou l'écran touché, et une
reproduction minimale. Si l'avis privé n'est pas disponible, ouvrez une issue
publique qui décrit l'impact sans détails d'exploitation, et demandez un canal
privé.

Les versions prises en charge sont la dernière version publiée et la branche
`main` actuelle. N'incluez pas de courrier réel, de clé de fournisseur ni de
donnée personnelle dans un signalement.
