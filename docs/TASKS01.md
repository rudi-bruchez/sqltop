<!-- task-status: done; date: 2026-09-11 -->
> Traité le 2026-09-11 : les trois défauts d'interface sont corrigés et testés, et la page de connexion est implémentée sur la branche `feat/connect-page`. Détail en fin de fichier.

# Améliorations

- Il faut un moyen de bâtir la chaîne de connexion de façon un petit peu graphique. Que faire lorsqu'on a une instance nommée ? Comment choisir l'authentification Windows ?... Il faudrait un petit TUI pour faire ça. ou éventuellement une page html de connexion. Quelle serait la meilleure solution ?
- lorsqu'on utilise la touche y Pour l'historique et qu'on n'a pas activé l'historique, il y a un message en fond de page mais qui ne disparaît pas. Et si on essaie de le faire disparaître avec la touche y, ça ne fait rien.
- Lorsqu'on sélectionne une requête et qu'on regarde par exemple son plan d'exécution ou son historique ou son texte, il faudrait que ce soit clairement surligné. On ne sait pas vraiment ce qu'on a sélectionné dans la liste des process.
- le rafraîchissement ne fonctionne pas sur les vues transactions et transaction log, et aussi vérifier sur les sessions.

## Traitement (2026-09-11)

1. Chaîne de connexion graphique : une page de connexion dans l'interface web existante plutôt qu'un TUI, qui ajouterait une dépendance et une seconde interface. Spec `docs/specs/2026-09-11-connect-page-design.md` et plan `docs/plans/2026-09-11-connect-page.md`, chacun relu par un panel de cinq lecteurs et corrigé. Kerberos n'y figure pas : la bibliothèque en Go pur ne lit pas le `krb5.conf` d'origine de Fedora et RHEL. Implémentée en huit tâches par sous-agents, chacune relue, puis une relecture de toute la branche (`feat/connect-page`, de `21a396b` à `4d26c64`). Fait.
2. Message de la touche `y` qui ne disparaît pas : reproduit en ouvrant l'historique puis en passant sur une vue liste, où le badge `paused` restait affiché alors que la liste se rafraîchissait, et où `y` ne pouvait pas l'enlever. `setView` recale désormais le gel, le badge et le message, et relance le panneau de détail au retour sur la grille (`internal/web/assets/app.js`). Assertion ajoutée au test navigateur (`e2e_test.go`, `e2e-driver.js`). Fait.
3. Ligne sélectionnée peu visible : fond bleu franc et barre d'accent sur la première cellule (`internal/web/assets/style.css`), vérifié sur capture d'écran. Fait.
4. Rafraîchissement des vues transactions, journaux et sessions : les trois vues interrogent bien le serveur toutes les 5 secondes (mesuré). Sessions avait un vrai défaut : une connexion en cours d'ouverture porte `1900-01-01` dans `login_time` et `last_request_end_time`, `DATEDIFF(second)` déborde et toute la liste tombe en erreur. Corrigé par `NULLIF` dans `sessionsQuery` (`internal/source/mssql/views.go`), avec un test qui échoue sans le correctif. Pour transactions et journaux, aucun arrêt du rafraîchissement n'a été reproduit ; la cause la plus probable est le badge `paused` du point 2. La barre d'état de ces vues affiche maintenant le nombre de lignes et l'heure de lecture, pour que le rafraîchissement se voie. Filtre `is_user_process = 1` des sessions vérifié contre le serveur. Fait, sous réserve d'un scénario non reproduit.
