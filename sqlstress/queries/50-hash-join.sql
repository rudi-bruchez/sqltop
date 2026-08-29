-- A 300000 row self join on a column with no index. Runs for seconds and
-- usually goes parallel, which is what the DOP column is there to show.
SELECT COUNT(*) AS n, MIN(a.Email) AS premier
FROM Contact.ProspectUS AS a
    JOIN Contact.ProspectUS AS b ON b.Email = a.Email;
