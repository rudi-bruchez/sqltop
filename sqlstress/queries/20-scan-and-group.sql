-- A full scan of 300000 rows with a grouping on top. Moderate elapsed time,
-- large logical reads: this is what the reads column is for.
SELECT p.Ville, COUNT(*) AS n
FROM Contact.ProspectUS AS p
GROUP BY p.Ville
ORDER BY n DESC;
