-- Holds exclusive locks on a third of Stage.Session for a few seconds, so
-- that 70-blocked-reader has something to wait behind. The update is a no-op
-- in value terms and the transaction always rolls back: sqlstress is allowed
-- to lock the demonstration database, not to change it.
--
-- If the run is cut short while this batch is waiting, the driver's attention
-- aborts it with the transaction still open. The connection goes back to the
-- pool, where the TDS reset rolls it back, and closing the pool at exit does
-- the same. Nothing survives the process.
BEGIN TRANSACTION;

UPDATE Stage.Session SET Note = Note WHERE SessionId % 3 = 0;

WAITFOR DELAY '00:00:03';

ROLLBACK TRANSACTION;
