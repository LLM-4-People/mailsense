---
layout: default
title: Installation
lang: fr
---

# Installer Mailsense

<!-- nav:start -->
[Documentation](index.md) · **Installation** · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../install.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


Mailsense lit le courrier en direct depuis le profil Outlook classique déjà
ouvert sur une machine Windows. Il ne demande pas d'identifiants Microsoft et
n'a pas de compte infonuagique. La version Linux est un aperçu : elle démarre,
affiche l'interface et ouvre les réglages, mais elle n'a pas encore de source de
courrier en direct.

| Capacité | Windows | Linux |
| --- | --- | --- |
| Courrier en direct depuis Outlook classique | Oui | Non, aperçu seulement |
| Moteur local | Téléchargé au premier usage | Téléchargé au premier usage |
| Installateur | Installateur par utilisateur | Archive avec `install.sh` |

## Windows

1. Téléchargez l'installateur depuis la page des versions.
2. Lancez-le. L'installation est par utilisateur et ne demande pas de droits
   administrateur.
3. La base et les réglages sont dans `%LOCALAPPDATA%\Mailsense`. La
   désinstallation les conserve, sauf si vous supprimez ce répertoire.

Mailsense a besoin du runtime Microsoft Edge WebView2. Windows 11 et les
Windows 10 à jour l'ont déjà. S'il manque, l'installateur vous l'indique et
renvoie vers la page du runtime Evergreen de Microsoft ; l'application ne peut
pas ouvrir de fenêtre avant son installation. Les versions publiées depuis ce
dépôt ne sont pas encore signées, Windows peut donc demander une confirmation.

## Linux

1. Téléchargez l'archive `amd64`.
2. Décompressez-la et lancez `./mailsense`, ou `./install.sh` pour une entrée de
   menu. Le répertoire de données est
   `${XDG_DATA_HOME:-~/.local/share}/mailsense`.
3. Le courrier en direct n'est pas encore offert sous Linux. La version est un
   aperçu de l'interface et du modèle de données local.

Le répertoire de données est choisi au lancement. Définissez
`MAILSENSE_DATA_DIR` avant de démarrer l'application pour en utiliser un autre ;
ce n'est pas un champ des réglages.

## Prérequis

- Une machine 64 bits. 4 Go de RAM est un minimum pratique ; 8 Go sont
  recommandés pour le modèle local.
- Un espace disque d'environ trois fois la taille de la boîte indexée : le
  courrier, l'index et l'espace de travail pendant une reconstruction. C'est
  une indication ; l'application n'applique que son plafond de disque.
- Le moteur local et son modèle ajoutent environ 342 Mo sous Windows et
  328 Mo sous Linux, téléchargés une fois.

## Vérifier un téléchargement

Chaque version publie les fichiers, un fichier `SHA256SUMS` qui contient
l'empreinte SHA-256 de chaque autre fichier, et un SBOM CycloneDX nommé
`mailsense-<version>-sbom.cdx.json`. Le manifeste ne se liste pas lui-même.

Sous Linux, depuis le répertoire des fichiers téléchargés :

```bash
sha256sum -c SHA256SUMS --ignore-missing
```

`--ignore-missing` ne vérifie que les fichiers téléchargés. Omettez l'option pour
exiger tous les fichiers du manifeste.

Sous Windows, comparez un fichier avec `Get-FileHash` dans PowerShell.

Chaque fichier listé dans `SHA256SUMS` a aussi une attestation de provenance
signée par GitHub Actions. Avec le CLI GitHub :

```bash
gh attestation verify mailsense-<version>-linux-amd64.tar.gz --repo LLM-4-People/mailsense
```

L'attestation indique le workflow, le commit et l'étiquette qui ont produit le
fichier. Ce n'est pas une signature de code : les binaires ne sont pas signés
Authenticode, Windows peut donc encore demander une confirmation.

## Désinstaller

- Windows : Paramètres, Applications, Mailsense, Désinstaller. Vos données
  restent dans `%LOCALAPPDATA%\Mailsense` jusqu'à ce que vous les supprimiez.
- Linux : supprimez le binaire et l'entrée de bureau. Les données sont dans
  `${XDG_DATA_HOME:-~/.local/share}/mailsense`.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
