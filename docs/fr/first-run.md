---
layout: default
title: Premier démarrage
lang: fr
---

# Premier démarrage

<!-- nav:start -->
[Documentation](index.md) · [Demander](ask.md) · [Installation](install.md) · **Premier démarrage** · [Console](console.md) · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../first-run.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


Le premier démarrage ouvre un court écran d'installation. Il enregistre un
marqueur à la fin, puis ne s'ouvre plus. Vous pouvez le rouvrir en réinitialisant
`ui.setup.done` dans les réglages.

## Parcours simple

1. **Bienvenue.** Mailsense affiche le répertoire de données, le nombre de
   boîtes Outlook déjà trouvées, la langue de l'interface et le fuseau horaire
   détecté. Rien n'est encore lu dans le courrier. Confirmez ou changez la langue
   et le fuseau ; les échéances et les réponses « aujourd'hui » en dépendent.
2. **Courrier.** Choisissez Chercher les boîtes pour lister les boîtes du profil
   Outlook classique de cette machine Windows, puis choisissez celle que Mailsense
   peut lire. Rien n'est lu avant votre choix. Choisissez Pas maintenant pour
   terminer sans courrier. Sous Linux, il n'y a pas encore de source en direct :
   cette étape l'explique et vous laisse terminer.
3. **Modèle.** Le moteur local est téléchargé au premier usage : environ 342 Mo
   sous Windows, 328 Mo sous Linux. Vous pouvez plutôt ajouter un fournisseur
   distant, ou passer cette étape et le faire plus tard dans Réglages,
   Fournisseurs. Le triage a besoin d'un décideur et répond
   `422 provider_not_configured` sans lui ; la recherche fonctionne sans décideur,
   et le classifieur local apprend quand même de vos réponses.
4. **Terminer.** Mailsense affiche les plafonds par défaut : 80 % du CPU, 85 % de
   la mémoire et 90 % du disque. Gardez-les sauf si vous connaissez bien votre
   machine. La première synchronisation et la première indexation tournent en
   arrière-plan. Les écrans qui lisent un index partiel le disent. Le seuil
   d'apprentissage reste fermé jusqu'à ce que vos propres réponses le certifient ;
   l'écran Apprentissage indique ce qu'il attend.

## Parcours avancé

L'écran d'installation renvoie aux réglages, qui ont trois onglets :

- **Général.** Chaque réglage déclaré, dans son groupe, avec sa valeur par
  défaut, sa valeur actuelle, sa couche et une action de réinitialisation.
- **Fournisseurs.** Les fiches de fournisseurs, le rôle de chacun et la liste de
  modèles récupérée auprès du fournisseur.
- **Moteur local.** L'état du moteur, son installation et sa vérification.

L'étape Modèle a un bouton Ouvrir Fournisseurs : le parcours avancé part du même
endroit.

Le parcours avancé n'est pas un second stockage de configuration : il écrit les
mêmes réglages que l'onglet Général.

## Ce qui est prêt avant de commencer

- Le fuseau horaire proposé par le système.
- Les plafonds de ressources déclarés avec l'application.
- Le profil de décision et la taxonomie par défaut fournis avec l'application.
- La liste des boîtes issue de la découverte.
- L'état du moteur local, s'il est déjà installé.

Mailsense ne précharge pas un catalogue de fournisseurs ni une liste de modèles :
un fournisseur est une fiche que vous ajoutez, et sa liste de modèles est
récupérée quand vous le demandez.

## Si une étape échoue

Chaque étape échoue séparément et peut être réessayée sans perdre les autres.
L'erreur nomme le réglage ou le point de terminaison, pas seulement un code
interne. Voir [dépannage](troubleshooting.md).

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
