-- A leading wildcard on a large table: the classic query that cannot use an
-- index and scans everything. Useful as the thing a demonstration points at
-- and says, that one.
--
-- Contact.ProspectUS_N, not Contact.ProspectUS_MAX. The latter carries a
-- corrupt page in the demonstration backup, so any scan of it fails with
-- error 824 rather than producing load. See sqlstress/README.md.
SELECT COUNT(*) AS n
FROM Contact.ProspectUS_N AS p
WHERE p.Email LIKE '%.com';
