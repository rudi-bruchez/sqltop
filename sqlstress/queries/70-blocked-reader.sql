-- Scans the table 60-blocker-solo has locked. Whenever the two land together
-- this session goes to LCK_M_S and shows up blocked, which is the case the
-- blocking view exists for.
--
-- READCOMMITTEDLOCK is what makes that happen. PachadataFormation has
-- read committed snapshot on, so a plain read takes its row versions from
-- tempdb and never waits for anyone. The hint asks for the locking flavour of
-- read committed on this statement alone, which produces the blocking without
-- changing a database setting on a database that is not ours.
SELECT COUNT(*) AS sessions, AVG(CONVERT(int, s.Note)) AS note_moyenne
FROM Stage.Session AS s WITH (READCOMMITTEDLOCK)
WHERE s.Statut IS NOT NULL;
