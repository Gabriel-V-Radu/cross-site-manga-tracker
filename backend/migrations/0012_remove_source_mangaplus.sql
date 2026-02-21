UPDATE sources
SET enabled = 0,
    updated_at = CURRENT_TIMESTAMP
WHERE key = 'mangaplus';
