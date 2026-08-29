-- A single-row seek. It is here so the workload is not made only of monsters:
-- sqltop should show these appearing and vanishing between two refreshes,
-- never sitting in the grid.
DECLARE @id int = ABS(CHECKSUM(NEWID())) % 35000 + 1;

SELECT c.ContactId, c.Nom, c.Prenom, c.Email, c.SocieteId
FROM Contact.Contact AS c
WHERE c.ContactId = @id;
