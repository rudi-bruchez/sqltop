-- The shape of a real reporting query: several joins, an aggregate, an order
-- by. Short enough to complete, long enough to be caught by a one second
-- refresh.
SELECT st.StageId, st.Categorie, s.DateDebut,
       COUNT(i.InscriptionId) AS inscrits,
       SUM(s.Prix) AS produit
FROM Stage.Session AS s
    JOIN Stage.Stage AS st ON st.StageId = s.StageId
    LEFT JOIN Inscription.Inscription AS i ON i.SessionId = s.SessionId
WHERE i.DateAnnulation IS NULL
GROUP BY st.StageId, st.Categorie, s.DateDebut
ORDER BY inscrits DESC, s.DateDebut DESC;
