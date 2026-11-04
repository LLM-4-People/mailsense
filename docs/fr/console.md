---
layout: default
title: Console
lang: fr
---

# Console

<!-- nav:start -->
[Documentation](index.md) · [Demander](ask.md) · [Installation](install.md) · [Premier démarrage](first-run.md) · **Console** · [Boîtes](mailboxes.md) · [Fournisseurs](providers.md) · [Apprentissage](learning.md) · [FAQ](faq.md) · [Confidentialité](privacy.md) · [Sécurité](security.md) · [Dépannage](troubleshooting.md) · [Contribuer](contributing.md) · <a href="../console.html" hreflang="en" lang="en" data-lang-switch="en">English</a>
<!-- nav:end -->


La console est la fenêtre locale de Mailsense. Chaque destination lit l'API
locale et affiche ce que la lecture a renvoyé, y compris un index partiel et une
tâche en échec. Cette page décrit la navigation, chaque écran, les états affichés
quand une lecture manque ou qu'un accès est bloqué, et les liens entre les
écrans.

## Navigation

La barre latérale compte cinq groupes repliables. L'ordre ci-dessous est celui
de la fenêtre.

| Groupe | Destinations | Contenu |
| --- | --- | --- |
| Travail | Accueil, Synthèse, Courrier, Entreprises | Le courrier classé, le travail en cours et les organisations |
| Questions | Demander, Réponses | Les questions en langage courant et l'historique des réponses |
| Enseigner | Enseigner, Apprentissage | Un message à la fois et la chronologie de l'apprentissage |
| Configurer | Sources, Réglages | Les boîtes aux lettres, l'identité, les fournisseurs et le moteur |
| Système | Journal | Le journal en mémoire et ses filtres |

Réponses est une destination à part entière sous Questions. Ce n'est pas un
onglet d'Enseigner, et Enseigner garde son propre lien vers elle.

Chaque groupe est un bloc natif repliable. L'ouverture ou la fermeture est
conservée dans le stockage local du navigateur. Le groupe qui contient l'écran
actif reste ouvert tant que cet écran est affiché. Quand le stockage local est
indisponible, tous les groupes s'ouvrent.

Dans une fenêtre étroite, les mêmes destinations passent dans un tiroir ouvert
par le bouton Menu. Le bouton expose son état avec `aria-expanded` ; choisir une
destination ferme le tiroir, et Échap le ferme et rend le focus au bouton.

Le pied de la barre latérale affiche l'état de connexion, la version, l'heure de
lancement et le répertoire de données. Quitter arrête l'API locale et ferme la
fenêtre. C'est le seul contrôle du processus dans la console.

### Clavier

| Touche | Effet |
| --- | --- |
| Tab et Maj+Tab | Parcourir la page dans l'ordre de lecture |
| Aller au contenu | La première tabulation atteint le lien d'évitement ; Entrée place le focus sur la région principale |
| `Ctrl+K` ou `Cmd+K` | Ouvrir Demander et placer le curseur dans le champ de question |
| `/` | Placer le focus dans la recherche, sauf si le curseur est déjà dans un champ de texte |
| Flèche haut et flèche bas | Parcourir les suggestions de recherche |
| Entrée | Ouvrir la suggestion sélectionnée ou lancer la recherche |
| Échap | Fermer la liste de suggestions, quitter la recherche, fermer le tiroir ou fermer une boîte de dialogue |

Demander accepte aussi Entrée pour envoyer, Maj+Entrée pour revenir à la ligne
et Alt+N pour commencer une nouvelle conversation. Enseigner accepte 1 à 9 pour
choisir une réponse, Entrée pour enregistrer et avancer, G pour utiliser la
suggestion du juge, S pour passer et ? pour afficher ou masquer l'aide des
raccourcis.

## Assistant de premier démarrage

L'assistant est une surcouche, pas une destination de navigation. Il s'ouvre une
fois après les lectures de lancement, recouvre la coque et suit quatre étapes :
Bienvenue, Courrier, Modèle et Terminer. Bienvenue affiche le répertoire de
données, les boîtes Outlook déjà trouvées, la langue et le fuseau horaire.
Courrier découvre et sélectionne des boîtes. Modèle propose le moteur local, un
fournisseur ou un passage. Terminer écrit le marqueur de fin et explique les
plafonds de ressources par défaut.

L'assistant s'ouvre quand `ui.setup.done` est faux et qu'aucune boîte aux lettres
n'est sélectionnée. Une boîte trouvée par la découverte mais non sélectionnée ne
le ferme pas. Pas maintenant à l'étape Courrier permet de terminer sans boîte,
et Terminer pour l'instant ferme l'assistant.

La console n'offre aucun contrôle pour rouvrir l'assistant. Pour le revoir,
réinitialisez `ui.setup.done` à faux dans Réglages, retirez toutes les boîtes aux
lettres dans Sources, puis quittez et rouvrez Mailsense. Réinitialiser le
marqueur seul ou retirer les boîtes seul ne suffit pas, car les deux conditions
doivent être réunies au lancement.

## Accueil

Accueil répond à ce qui mérite votre attention maintenant. Il réunit les totaux
de la Synthèse, les prochains éléments de l'agenda, le courrier important et un
résumé de l'apprentissage.

L'en-tête affiche l'heure de génération et trois compteurs : réponses dues,
échéances et réunions. La section agenda propose les horizons de 3 h, 8 h et
24 h, plus la valeur par défaut configurée quand elle diffère ; un bouton
recharge l'agenda pour cette fenêtre. La section Synthèse liste les trois
signaux les mieux classés, et Ouvrir le courrier mène à Courrier. Réviser les
suggestions ouvre Enseigner en mode révision pour la question qui en a besoin.
Le bloc Plus contient les questions récentes, les réponses récentes, la
progression de l'apprentissage et le résumé de la boîte, chacun avec son lien.

Quand le verrou d'apprentissage est fermé, Accueil devient un tableau de bord.
Il montre l'objectif de réponses manuelles, les questions restantes et leur
progression, avec Réviser les suggestions et Répondre à des questions. Le
courrier important reste verrouillé. Le volet d'apprentissage affiche le premier
blocage, Ouvrir l'apprentissage et le bouton Aperçu de test. L'aperçu de test
ouvre les écrans de réponses automatiques sans rien certifier ; tant qu'il est
actif, un bandeau propose Ouvrir le courrier et Désactiver l'aperçu.

Un élément manquant ou en échec reste limité à sa section. Un agenda qui n'a pas
encore répondu affiche un tiret dans les compteurs et un squelette de chargement
dans la section ; un agenda en échec se dit indisponible, et un autre bouton
d'horizon relance la lecture. Un échec du courrier important propose Réessayer.
Un échec des questions récentes conserve les réponses récentes et propose
Réessayer au lieu d'affirmer qu'il n'y a aucune activité.

Écrans liés : Synthèse, Courrier, Entreprises, Enseigner, Apprentissage,
Sources, Demander et Réponses. Le bloc Plus mène directement à Demander et à
Réponses.

## Synthèse

La Synthèse répond à ce qui est ouvert dans le travail en cours : les
engagements qui vous attendent, les réponses que vous attendez, l'activité
depuis la dernière limite vue, les relations ouvertes et les sujets suggérés.

Les quatre cartes de statistiques mènent à leur section. Chaque ligne ouvre son
message dans Courrier ou son organisation dans Entreprises. Marquer comme vu,
dans la section Modifications, enregistre l'heure de génération courante comme
prochaine limite et recharge la Synthèse ; le bouton reste désactivé tant
qu'aucune heure de génération n'est connue. La section Sujets signale les sujets
suggérés et affiche la couverture de classification.

Si le verrou ne peut pas être lu, l'écran affiche une erreur avec Réessayer et
rien d'autre. Si le verrou est fermé, l'écran affiche les blocages
d'apprentissage et Ouvrir l'apprentissage. Une lecture de la Synthèse en échec
propose Réessayer. Si le navigateur refuse d'enregistrer la nouvelle limite,
Marquer comme vu signale que la date n'a pas pu être enregistrée et conserve
l'ancienne limite.

Écrans liés : Courrier, Entreprises, Apprentissage et Enseigner.

## Courrier

Courrier répond à ce que contient la boîte, ce qui est important, quelles
conversations sont ouvertes et ce qu'une recherche a trouvé.

Les onglets sont Tout le courrier, Importants, Conversations et Récents. Tout le
courrier filtre par rôle de dossier, état de lecture et adresse d'expéditeur ou
`@domaine` ; les filtres d'expéditeur et de non lus affichent une action
d'effacement quand ils sont actifs. Importants est la liste classée et porte ses
propres filtres : à répondre, urgent, sécurité, décision, échéance et charge.
Conversations filtre par sujet, expéditeur et état de la conversation. Récents
lit le courrier trié le plus récent une page à la fois. Chaque liste paginée a
Charger plus et indique quand elle atteint la fin ou son plafond.

La recherche se trouve dans la barre supérieure, pas dans un onglet. La saisie
affiche des suggestions ; Entrée ouvre le résultat sélectionné ou la page de
résultats complète dans Courrier. La page de résultats peut filtrer par boîte,
rôle de dossier et pièces jointes. Sur un écran qui possède sa propre liste, la
même zone filtre cette liste.

Sélectionner un message ouvre le volet de détail. Afficher le message complet
déplie le corps ; les images distantes restent bloquées et le volet indique leur
nombre. Poser une question sur ce message ouvre Demander avec une question sur
ce message, et Ouvrir dans Enseigner ouvre l'atelier d'étiquetage sur la
première question. Une carte de conversation affiche son état, ses participants
et les messages précédents.

Une lecture du verrou en échec affiche une erreur avec Réessayer ; l'écran ne
déduit jamais la permission d'un état d'apprentissage périmé. Un verrou fermé
affiche la liste des blocages et Ouvrir l'apprentissage, ou une action
d'actualisation quand aucun blocage n'est signalé. Un dossier vide le dit et
propose un autre dossier ou la prochaine synchronisation. Une
liste Importants vide propose Réviser les suggestions. Un échec d'actualisation
conserve la dernière liste et signale qu'elle n'a pas pu être actualisée. Une
recherche sans résultat propose d'autres mots ou une question plus courte.
Quand aucun message n'est sélectionné, le volet de détail demande d'en choisir
un.

Écrans liés : Enseigner, Demander, Entreprises, Apprentissage et Sources.

## Entreprises

Entreprises répond à qui sont les organisations, à quoi ressemble la relation et
à ce qui reste ouvert avec chacune.

La liste est classée selon le courrier stocké. La recherche couvre tout le
classement sur le serveur, donc une entreprise au-delà de la première page reste
accessible ; le champ filtre aussi immédiatement les lignes visibles pendant la
requête. Sélectionner une ligne ne remplace que le volet du profil. Interroger
cette entreprise ouvre Demander avec une question préparée. Les entrées du
profil ouvrent leur message dans Courrier.

Le profil affiche le résumé de preuve, la prochaine étape, les chiffres clés,
les fils ouverts selon qui a parlé en dernier, les engagements dus, les réunions
à venir, le courrier récent, les adresses et le volume mensuel. Une organisation
déclarée et une organisation de domaine portent des badges différents. Le
courrier à sens unique est conservé et nommé au lieu d'être masqué.

Un verrou fermé affiche les blocages d'apprentissage et Ouvrir l'apprentissage.
Sans entreprise, l'état vide explique que les adresses personnelles et de
messagerie gratuite ne sont pas regroupées. Une recherche sans résultat propose
Effacer la recherche. Une lecture de liste en échec propose Réessayer. Une
lecture de profil en échec reste dans le volet du profil avec Réessayer et ne
fait pas disparaître la liste. Quand l'entreprise sélectionnée sort de la recherche
courante, le volet le dit au lieu de laisser deux volets se contredire.

Écrans liés : Courrier, Demander et Apprentissage.

## Demander

Demander répond à une question sur le courrier en langage courant. La réponse
peut être un comptage ou une liste calculés, des messages correspondants, ou une
réponse rédigée avec des citations.

Le compositeur a un champ de question et un bouton Envoyer. Le sélecteur de
modèle propose Auto, Local et Distant. Auto répond localement quand il le peut
et utilise sinon le fournisseur configuré ; Local n'appelle jamais de modèle ;
Distant demande au fournisseur nommé de rédiger la réponse. Des questions
suggérées remplissent le champ. Nouvelle conversation, ou Alt+N, commence un
nouveau fil, et l'historique latéral rouvre une réponse antérieure. Les citations
ouvrent le message qu'elles nomment.

Un réglage Q&R désactivé affiche la raison et un lien vers ce réglage. Un verrou
fermé affiche Ouvrir l'apprentissage et le bouton Aperçu de test. Une option
distante non configurée, inutilisable ou inconnue le dit et renvoie aux réglages
des fournisseurs, tandis qu'Auto et Local restent disponibles. Un index de
recherche encore en préparation ou un délai de recherche dépassé garde la
question et propose Réessayer. Un échec de rédaction ou une
réponse qui n'a pas pu être ancrée conserve les messages correspondants et nomme
le fournisseur. Sans
correspondance, l'écran propose d'autres mots, une question plus courte ou un
sujet plus large. Une question trop courte est refusée dans le champ. Une
réponse rédigée dans une autre langue est affichée telle quelle, avec ses
citations, et l'avis précise que redemander appelle de nouveau le fournisseur.

Écrans liés : Réponses, Courrier, Réglages (Fournisseurs) et Apprentissage. Le
guide complet couvre les trois modes, l'ancrage, la requête au fournisseur et les
limites : [Demander](ask.md).

## Réponses

Réponses montre ce qui a déjà été répondu, par vous ou par le juge, pour chaque
question.

Le sélecteur de question filtre par question et le contrôle segmenté filtre par
source : Toutes, Vous ou Juge. Réviser les suggestions ouvre Enseigner en mode
révision. Une ligne ouvre le détail du message et son historique de réponses
pour cette question ; Lire dans Enseigner et Réviser / corriger dans Enseigner
mènent au même message dans l'atelier. La liste se pagine avec Charger plus et
signale sa fin.

Une lecture de liste en échec propose Réessayer. Un filtre vide explique que les
réponses apparaissent une fois stockées et propose une autre question ou une
autre source. Une lecture de détail en échec le signale dans le volet de détail,
et un message sans réponse stockée le dit.

Écrans liés : Enseigner, Courrier et Apprentissage.

## Enseigner

Enseigner répond à comment étiqueter un message, une question à la fois, et
transforme la réponse en exemple stocké.

Le sélecteur de mode propose Étiqueter et Réviser les suggestions. Étiqueter
vous demande de classer le message affiché. Réviser les suggestions montre
d'abord la suggestion du juge, avec Utiliser la suggestion pour l'enregistrer
comme étiquette manuelle et avancer, ou une autre réponse en dessous. Les pastilles de question sont classées par
besoin restant et défilent avec les boutons fléchés ou les flèches gauche et
droite. Enregistrer et avancer stocke chaque question sélectionnée et passe à la
suite ; Passer laisse le message sans réponse. Précédent et Suivant parcourent
la file chargée. Le bloc Avancé affiche l'exigence d'étiquettes d'évaluation,
l'action Obtenir des suggestions du juge avec son coût estimé quand il est
signalé, Ouvrir l'apprentissage et le diagnostic du blocage. Le lien Historique
des réponses ouvre Réponses.

Chaque tâche affiche le sujet, l'expéditeur et le message. La file est une page,
pas la population entière : atteindre la fin d'une page propose Continuer, et
une vraie fin propose Question suivante et Retour à l'accueil. Quand le profil
ne publie aucune question, l'écran le dit et renvoie au fichier de profil dans
Réglages. Quand les faits de la machine ne sont pas confirmés, l'écran nomme le
blocage du fuseau horaire et propose son action au lieu de construire une file.
Une lecture de file en échec propose Réessayer sans reconstruire la barre ni
perdre la question sélectionnée. Une lecture de résumé en échec laisse les
compteurs à un tiret et garde la file utilisable.

Clavier : 1 à 9 choisit une option ou un niveau, Entrée enregistre et avance, G
utilise la suggestion du juge quand elle existe, S passe, et ? affiche ou masque
l'aide des raccourcis.

Écrans liés : Apprentissage, Réponses et Courrier.

## Apprentissage

Apprentissage répond à pourquoi la boîte peut ou ne peut pas encore répondre, et
à ce qui la fait avancer.

L'encart affiche l'état de l'apprentissage, le verrou, le nombre de
certifications et les prochaines actions. Les actions dépendent de l'état :
Ouvrir le courrier quand le verrou est ouvert ; Répondre à une question et
Réviser les suggestions tant que l'étape des étiquettes n'est pas terminée ; et
l'action de lancement proposée par un blocage, comme Obtenir des suggestions du
juge, Lire les indices du courrier ou Entraîner le classifieur. Les liens de
question ouvrent les trois questions qui aident le plus. Le volet latéral
affiche l'avancement des étapes, le nombre de blocages, les tâches en cours avec
un lien vers le Journal, la progression des étiquettes, le périmètre, le fuseau
horaire, la dernière mise à jour et Actualiser.

La chronologie liste chaque étape avec son état, sa progression, son unité, son
estimation et son détail technique. Chaque blocage nomme son propre remède : une
exécution en échec renvoie au Journal, un blocage de configuration renvoie à la
ligne de Réglages correspondante, un moteur de plongements absent renvoie à
l'onglet Moteur local, et un fuseau horaire non confirmé renvoie au réglage du
fuseau. La ligne de périmètre dit quand aucune boîte aux lettres n'est
sélectionnée, et la ligne de fuseau dit quand il vient de la machine et non
d'un choix confirmé du propriétaire.

Quand aucune étape n'est signalée, la chronologie le dit et Actualiser la relit.
Un résumé d'étiquettes en échec est affiché comme inconnu, pas comme zéro. Une
exécution en aperçu porte un bandeau qui précise que rien n'est certifié.

Écrans liés : Enseigner, Courrier, Journal, Réglages et Sources.

## Sources

Sources répond à quelles boîtes aux lettres Mailsense peut lire, qui est le
propriétaire, et dans quel état se trouvent chaque magasin, chaque suppression
et chaque relecture d'affichage.

Le sélecteur de source choisit le connecteur, Découvrir lit sa liste de magasins
sans lire de message, Synchroniser lance une synchronisation pour la source
sélectionnée, et Actualiser relit l'écran. Le tableau des boîtes est triable et
contient une ligne par magasin. La case de sélection ajoute un magasin au
périmètre d'apprentissage et lance une synchronisation pour sa source, ou le
retire de ce périmètre. Retirer demande une confirmation et explique que le
courrier stocké est conservé pour une nouvelle sélection.

La colonne d'identité du propriétaire liste les adresses signalées par la source
et celles que vous avez déclarées. Déclarer ouvre une boîte de dialogue et
valide l'adresse ; une adresse déclarée se retire avec le bouton de sa pastille.
Un magasin sans adresse affiche Aucune adresse de propriétaire, ce qui bloque
l'apprentissage jusqu'à la déclaration d'une adresse.

Chaque magasin affiche son état sous forme de badges : en ligne ou hors ligne,
en cache, par défaut, non découverte et sélectionnée. Un badge hors ligne porte
l'erreur du connecteur dans son infobulle. Une découverte ou une synchronisation
en échec signale un avis et laisse le tableau dans son dernier état connu. Une
tâche de synchronisation ou de relecture d'affichage en échec est nommée
au-dessus des liens croisés avec Ouvrir la tâche en échec dans les journaux.

La section Messages en attente de confirmation liste chaque dossier où une
synchronisation complète a trouvé des messages supprimés. Confirmer la
suppression nomme le dossier et le nombre dans une boîte de dialogue
d'avertissement, puis applique la suppression. La carte indique quand la suppression s'applique
automatiquement. Une lecture en échec des suppressions en attente garde son
propre bouton Réessayer au lieu d'afficher une carte vide.

Affichage des messages lance Récupérer l'affichage manquant pour la source
sélectionnée, montre l'avancement et le total pendant l'exécution, affiche son
dernier message de progression et toute erreur, et propose Annuler par
l'annulation ordinaire d'une tâche. Le bouton est désactivé pendant une tâche
d'affichage ou quand aucune source n'est enregistrée. Le panneau explique que
cette relecture ne stocke que le HTML d'affichage et les images intégrées, et
laisse intact le corps utilisé pour l'apprentissage.

Le tableau du registre des sources liste les connecteurs connus de ce
processus, avec leur identifiant et leur type. Dans une version sans source en
direct, l'écran dit que la découverte est indisponible au lieu de laisser croire
que le tableau est vide faute de courrier.

### Actions de source dans l'API locale

L'API locale répond à trois actions du propriétaire sur une source, chacune par
`POST /api/sources/{id}/{action}` et chacune renvoyant l'action, la source et,
pour la nouvelle tentative, le nombre de messages remis de côté pour une lecture
de détail :

- `start` démarre Outlook classique comme le ferait le propriétaire, par la
  capacité de démarrage du connecteur. Un connecteur sans cette capacité répond
  `409 action_unsupported`.
- `resume` oublie les pertes de connexion enregistrées pour que l'opération
  suivante se connecte comme une première. Outlook doit tout de même être en
  cours d'exécution. Un connecteur sans la capacité de reprise répond
  `409 action_unsupported`.
- `retry-details` vide la liste des messages mis de côté pour cette source afin
  que sa prochaine synchronisation les relise, et indique combien étaient mis de
  côté. La source doit savoir lire le détail des messages, et l'action répond
  `503 unavailable` quand le service de synchronisation n'est pas disponible.

La console n'appelle pas encore ces trois interrupteurs. Quand un blocage
d'apprentissage propose Démarrer la source, Reconnecter, Reprendre la source ou
Réessayer les détails, la console ouvre un court guide puis exécute la vraie
action de découverte ou de synchronisation à sa portée.

Écrans liés : Réglages (Synchronisation et Filtres Outlook), Apprentissage et
Journal.

## Réglages

Réglages répond à ce que l'application est configurée pour faire et quelle couche
a fourni chaque valeur. La priorité est la valeur par défaut, puis la valeur
apprise, puis `settings.json`, puis l'environnement.

### Vue simple

Général s'ouvre sur la vue simple, sauf si vous êtes passé à la vue avancée
plus tôt dans la session. Simple et Avancé forment un sélecteur de vue, et le
choix dure toute la session. La vue simple montre les huit paramètres du
quotidien en quatre groupes : langue et heure (`ui.language`, `time.zone`),
Demander et suggestions (`qa.enabled`, `qa.max_messages`), courrier et dossiers
(`sync.details.enabled`, `triage.folder_roles`, `threads.folder_roles`) et
apprentissage (`learning.test_mode`). Afficher tous les paramètres passe à la
vue avancée, et la note indique combien de paramètres sont masqués.

### Vue avancée

La vue avancée liste chaque paramètre que possède l'onglet Général, groupé par
groupe du schéma. Chaque ligne montre la clé du paramètre, sa description, la
valeur effective, la valeur par défaut, le badge de couche et les actions
applicables :

- Appliquer écrit la valeur de la ligne. L'action est désactivée quand la valeur
  vient de l'environnement, car l'écrire dans `settings.json` ne changerait pas
  la valeur effective.
- Réinitialiser retire la valeur stockée et reste désactivé quand la valeur
  égale déjà sa valeur par défaut.
- Une ligne secrète n'affiche jamais la valeur stockée ; laissez-la vide pour
  conserver la valeur actuelle, ou saisissez-en une nouvelle pour la remplacer.
- Une ligne de fichier de données nomme le fichier externe et explique qu'il est
  lu à l'exécution, donc qu'une modification est prise en compte sans
  redémarrage.
- Une ligne apprise peut être rejetée, avec la valeur et le nombre
  d'observations affichés.
- Une ligne d'environnement nomme la variable `MAILSENSE_`, désactive Appliquer
  et indique qu'il faut retirer la variable puis redémarrer Mailsense pour
  utiliser la valeur du fichier.

La zone de recherche de la barre supérieure cherche dans tous les paramètres, y
compris ceux que possèdent les onglets Fournisseurs et Moteur local. Une
correspondance sur un autre onglet apparaît comme un renvoi avec sa valeur
actuelle et sa valeur par défaut, et Ouvrir l'onglet mène à la ligne. La barre
supérieure indique aussi si un redémarrage est en attente.

Une ligne qui porte le badge redémarrage requis exige de quitter puis de rouvrir
l'application. Dans ce schéma, ces lignes couvrent le stockage, le réseau, la
limite du pool de requêtes, le journal dans un fichier, le fichier de secrets,
l'intervalle de surveillance de la configuration, l'horizon par défaut de
l'agenda et le répertoire du moteur. Une valeur enregistrée qui attend ce
redémarrage apparaît dans un bandeau qui nomme les clés et explique exactement
ce que font les deux actions. Recharger depuis le disque relit `settings.json`
et actualise la console ; cela n'applique pas une valeur qui exige un
redémarrage. Seuls la fermeture et le redémarrage de Mailsense appliquent ces
valeurs. Le bandeau est le seul endroit où le bouton Recharger apparaît.

### Onglets propriétaires

Fournisseurs possède les enregistrements de fournisseurs et les quatre rôles :
Q&R, Juge, Triage et Plongements. Chaque rôle sélectionne un enregistrement du
type correspondant, et un rôle non configuré bloque sa fonction. Ajouter un
fournisseur ouvre un formulaire avec les champs de connexion, un protocole, un
modèle, un nom de clé et une section Avancé pour les limites, le prix, les
nouvelles tentatives et les champs propres au protocole. Récupérer les modèles
envoie une requête à la liste de modèles du fournisseur et remplit le formulaire
sans enregistrer. Tester envoie une requête qui peut être facturée et indique le
succès, la latence, le modèle et un court détail. Modifier change
l'enregistrement. Supprimer est refusé tant qu'un rôle le sélectionne, et la
confirmation précise que les appels qui l'utilisent échoueront jusqu'à la
configuration d'un autre enregistrement.

Moteur local possède le moteur géré. Il indique le modèle de plongements, l'état
(installation, démarrage, prêt, inactif, en échec, arrêté ou non sélectionné), la
taille installée, l'identifiant du processus, les fils, les redémarrages et la
dernière heure de démarrage. Il dit quand les plongements utilisent un
fournisseur à la place, avec un lien vers Fournisseurs. Le bloc Avancé propose
Réinstaller / Réparer quand la version ou le modèle est connu, et les réglages
du moteur s'affichent comme des lignes ordinaires. Actualiser relit l'état du
moteur. Une version cassée affiche sa raison ; une version Vulkan indisponible
explique l'échec du pilote ou de la sonde.

La sélection des boîtes et les adresses déclarées ne se modifient pas ici. Les
lignes de boîte renvoient à Sources, qui possède ces écritures.

Si la liste des fournisseurs échoue, l'écran le dit et permet encore d'ajouter
un enregistrement. Sans fournisseur, l'état vide s'affiche. Un rôle qui nomme un
enregistrement absent le dit. Si un test de fournisseur échoue, l'écran le
signale et renvoie aux entrées du moteur dans le Journal. Un moteur sans version
publiée indique que le catalogue est vide ; si la réparation échoue, l'erreur
s'affiche sur place.

Écrans liés : Sources, Apprentissage, Journal et les liens vers les fournisseurs
depuis Demander.

## Journal

Le Journal répond à ce que l'application a fait et pourquoi une tâche ou un appel
a échoué. Il lit l'anneau en mémoire et suit les nouvelles entrées par le flux
d'événements.

La barre d'outils filtre par niveau et par composant, met en pause ou reprend le
suivi en direct, efface la vue courante et actualise. Effacer la vue ne vide pas
l'anneau du serveur ; de nouvelles entrées continuent d'arriver. Un lien depuis
un autre écran peut ajouter un filtre de tâche, affiché comme une pastille avec
son action d'effacement. Déplier une ligne montre tous les attributs stockés ;
les valeurs qui ressemblent à des secrets ou à du contenu sont déjà masquées par
le journal.

Quand l'anneau est vide, le tableau le dit. Quand un filtre masque toutes les
entrées, il nomme le filtre. Un filtre de tâche sans entrée conservée précise que
l'anneau ne garde que les entrées les plus récentes. Un échec d'écriture du
journal dans un fichier, des événements perdus par des abonnés saturés, un
instantané tronqué ou une perte d'événements apparaissent dans un bandeau
au-dessus du tableau. Un attribut qui n'est plus conservé le dit quand sa ligne
est dépliée.

Écrans liés : Apprentissage, Sources et Réglages.

## Ce que la console ne fait pas encore

- Il n'y a pas de modèle multi-utilisateur ni de locataire. Un jeton de
  lancement, un répertoire de données et un propriétaire servent toute la
  console ; il n'y a ni comptes utilisateur, ni rôles utilisateur, ni séparation
  par utilisateur.
- Il n'y a pas de contrôle de redémarrage. Quitter arrête le processus. Un
  réglage qui exige un redémarrage ne prend effet qu'après avoir quitté puis
  relancé Mailsense.
- Les recherches ne sont pas enregistrées. Demander garde un historique de
  questions, mais la zone de recherche ne conserve aucune liste de recherches
  passées et il n'existe pas d'écran de recherches enregistrées.
- Il n'y a pas d'export. Les messages, les réponses, les données
  d'apprentissage et le journal se lisent dans la console, mais aucun écran ne
  les écrit dans un fichier.
- Il n'y a pas d'annulation par lot des étiquettes. L'API locale répond à une
  suppression d'étiquette unique par
  `DELETE /api/labels/{message_id}/{question}`, mais la console n'expose aucun
  contrôle d'annulation et il n'existe pas de restauration par lot.
- La version Linux n'a pas de source de courrier en direct. Elle ouvre tous les
  écrans, mais Sources explique que la découverte est indisponible en dehors
  d'Outlook classique sous Windows.
- Le comportement Windows n'est pas vérifié depuis une version Linux. La fenêtre
  WebView2 et la connexion à Outlook classique ne peuvent pas être exercées sur
  cet hôte, donc leur premier affichage et le comportement COM restent non
  vérifiés ici.

<!-- footer:start -->
[Code source](https://github.com/LLM-4-People/mailsense) · AGPL-3.0-or-later · [Documentation](https://llm-4-people.github.io/mailsense/fr/) · [Confidentialité](privacy.md)
<!-- footer:end -->
