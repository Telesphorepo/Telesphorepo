-- rev names the state of a project page: it moves on with each change of a file
-- (max(the rev before + 1, unix ns of the change)), so the rendered page is kept per revision.
ALTER TABLE pypi_projects ADD COLUMN rev INTEGER NOT NULL DEFAULT 0;