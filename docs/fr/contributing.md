---
layout: default
title: Contribuer
lang: fr
---

# Contribuer

[English](../contributing.md)

Mailsense est écrit en Go pour l'application et en JavaScript simple pour
l'interface. Il n'y a pas de cadre de travail de chaque côté. Le dépôt est la
source de vérité pour la compilation, les tests et les pages publiques.

## Prérequis

- Go 1.27 ou plus récent.
- Node.js pour les tests de comportement de l'interface. Ces tests sont sautés
  quand Node manque ; le reste s'exécute.
- Une chaîne d'outils C pour le détecteur de courses. `scripts/test.sh` active
  les courses quand cgo fonctionne.

## Compiler et tester

```bash
go build ./...
scripts/test-fast.sh    # boucle interne : format, vet, scripts, tests unitaires et de contrat
scripts/test.sh         # porte de version exhaustive
```

`scripts/test-fast.sh` est la porte normale avant un commit. `scripts/test.sh`
est celle à lancer avant une version. Les deux prennent leurs contrôles de
format, de scripts et de vet dans une bibliothèque commune.

Après un changement dans `internal/ui/js` ou `internal/ui/css`, régénérez les
paquets servis :

```bash
go generate ./internal/ui
```

Les tests des paquets échouent quand le `app.js` ou le `app.css` généré est
périmé.

## Pages publiques

`docs/site.json` est la source unique des pages d'accueil, des deux README et des
SVG de marque. Ne modifiez pas un fichier généré à la main.

```bash
make site           # régénère la surface publique
make site-check     # échoue quand elle est périmée
```

Une page de documentation vient en paire : `docs/name.md` et
`docs/fr/name.md`. Ajoutez la clé dans `internal/site/render.go`,
`internal/site/site.go`, le tableau `nav` de `docs/site.json` et
`docs/_config.yml`. L'empaquetage de version lit la liste des pages publiques
depuis `internal/site`, donc une nouvelle page est livrée sans deuxième copie de
la liste.

## Texte public

`internal/prose` possède la règle de style du texte livré aux lecteurs. Elle
refuse le tiret cadratin, le tiret demi-cadratin espacé, le double trait d'union
de ponctuation et une liste de formules toutes faites. Les blocs de code et le
code en ligne sont ignorés. Le test des pages et le test des catalogues de
l'interface utilisent ce même propriétaire.

Écrivez en anglais simple et en français simple. Préférez une phrase courte et
un fait concret à une promesse. Ne décrivez pas une fonction que le code
n'implémente pas.

## Changements d'interface

- Un module d'écran va dans `internal/ui/js/manifest.txt` et une feuille de style
  dans `internal/ui/css/manifest.txt`. Les deux manifestes sont complets, sinon
  les tests échouent.
- Chaque chaîne visible vient de `i18n.en.js` et de `i18n.fr.js`. Le catalogue
  français doit couvrir chaque réglage et chaque code d'erreur de l'API.
- Gardez la logique du domaine hors de l'interface. Appelez la route existante ;
  ne dupliquez pas une règle en JavaScript.

## Signaler un bogue

Indiquez la version, le système d'exploitation, l'écran ou la route, ce que vous
attendiez, ce qui est arrivé, et l'entrée pertinente des journaux. Retirez le
contenu du courrier, les adresses et les clés de fournisseur avant de coller un
journal.

Les signalements de sécurité passent par le canal privé de la [page
sécurité](security.md).

## Licence

Le projet est publié sous AGPL-3.0-or-later. En contribuant, vous acceptez que
votre contribution soit distribuée sous la même licence. Il n'y a pas d'accord
de licence de contributeur séparé.
