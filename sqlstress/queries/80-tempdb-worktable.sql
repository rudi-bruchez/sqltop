-- Materialises an aggregate into a temporary table, so the session holds real
-- user objects in tempdb rather than only a spilled sort.
SELECT p.Ville, p.CP, COUNT(*) AS n
INTO #prospects
FROM Contact.ProspectUS AS p
GROUP BY p.Ville, p.CP;

SELECT TOP (50) Ville, CP, n
FROM #prospects
ORDER BY n DESC;

DROP TABLE #prospects;
