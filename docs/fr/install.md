---
layout: default
title: Installation
lang: fr
---

# Installer Mailsense

<!-- nav:start -->
[Documentation](index.md) · [Demander](ask.md) · **Installation** · [Premier démarrage](first-run.md) · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../install.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->

La version publiée est un installateur Windows ; les sources se compilent aussi
sous Linux en aperçu, avec une boîte vide, et cette compilation n'est pas
proposée au téléchargement. Mailsense lit le courrier en direct depuis le profil
Outlook classique déjà ouvert sur une machine Windows, et il ne demande pas
d'identifiants Microsoft.

| Capacité | Windows |
| --- | --- |
| Courrier en direct depuis Outlook classique | Oui |
| Moteur local | Téléchargé au premier usage |
| Installateur | Installateur par utilisateur, sans administrateur |

## Windows

1. Téléchargez `mailsense-<version>-setup.exe` depuis la page des versions. La
   version vient de l'étiquette de version.
2. Lancez-le. L'installation est par utilisateur et ne demande pas de droits
   administrateur.
3. La base et les réglages sont dans `%LOCALAPPDATA%\Mailsense`. La
   désinstallation les conserve, sauf si vous supprimez ce répertoire.

Mailsense a besoin du runtime Microsoft Edge WebView2. Windows 11 et les
Windows 10 à jour l'ont déjà. S'il manque, l'installateur vous l'indique et
renvoie vers la page du runtime Evergreen de Microsoft ; l'application ne peut
pas ouvrir de fenêtre avant son installation. Les versions publiées depuis ce
dépôt ne sont pas encore signées, SmartScreen peut donc demander une
confirmation.

## Prérequis

- Une machine 64 bits. 4 Go de RAM est un minimum pratique ; 8 Go sont
  recommandés pour le modèle local.
- Un espace disque d'environ trois fois la taille de la boîte indexée : le
  courrier, l'index et l'espace de travail pendant une reconstruction. C'est
  une indication ; l'application n'applique que son plafond de disque.
- Le moteur local et son modèle ajoutent environ 342 Mo sous Windows. L'aperçu
  Linux compilé depuis les sources utilise le même moteur et le même modèle
  épinglés, environ 328 Mo, téléchargés une fois.

## Vérifier un téléchargement

Chaque version publie l'installateur, un fichier `SHA256SUMS` qui contient
l'empreinte SHA-256 de chaque autre fichier, et un SBOM CycloneDX nommé
`mailsense-<version>-sbom.cdx.json`. Le manifeste ne se liste pas lui-même.

Sous Windows, comparez l'installateur avec son entrée dans `SHA256SUMS` en
lançant `Get-FileHash .\mailsense-<version>-setup.exe` dans PowerShell.

Chaque fichier listé dans `SHA256SUMS` a aussi une attestation de provenance
signée par GitHub Actions. Avec le CLI GitHub :

```bash
gh attestation verify mailsense-<version>-setup.exe --repo LLM-4-People/mailsense
```

L'attestation indique le workflow, le commit et l'étiquette qui ont produit le
fichier. Ce n'est pas une signature de code : l'installateur n'est pas signé
Authenticode, Windows peut donc encore demander une confirmation.

## Désinstaller

Sous Windows, ouvrez Paramètres, Applications, Mailsense et choisissez
Désinstaller. Vos données restent dans `%LOCALAPPDATA%\Mailsense` jusqu'à ce que
vous les supprimiez.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
