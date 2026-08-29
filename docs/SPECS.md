# sqltop specs

- Outil graphique très léger et rapide en Go, multiplateformes et statique -- on utilise Gio ?
- S'ouvre directement en maximisé

## principes

- Surveillance en temps réel d'un serveur SQL.
- Base Agnostique sur le serveur.
- MVP pour SQL server

## Affichage

- serveur, OS, versions
- consommation CPU du SGBDR, globale et par CPU au besoin
- consommation de la RAM (buffer, cache de plans, mémoire de requêtes)
- activité tempdb
- nombre de scans de tables en temps réel

- affichage principal : liste des requêtes, avec les mêmes colonnes que sp_WhoIsActive

## Version après MVP :

- Suivi des sessions.
- stockage local pour un historique par exemple de quelques minutes, voire même un peu plus longtemps avec les sessions, les requêtes effectuées dans les différentes sessions, le plan d'exécution après exécution en profil
