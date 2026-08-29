-- Deliberately starved of memory so the sort spills to tempdb. The grant
-- hints are the whole point: without them SQL Server would size the grant
-- correctly and there would be nothing to show in the tempdb column.
SELECT TOP (200000) p.Nom, p.Prenom, p.Email, p.Adresse, p.Ville, p.CP
FROM Contact.ProspectUS AS p
ORDER BY p.Email, p.Nom, p.Prenom
OPTION (MAXDOP 1, MIN_GRANT_PERCENT = 0, MAX_GRANT_PERCENT = 1);
