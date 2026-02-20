ALTER TABLE trackers
ADD COLUMN rating REAL
    CHECK (
        rating IS NULL
        OR (
            rating >= 0
            AND rating <= 10
            AND ABS((rating * 2) - ROUND(rating * 2)) < 0.000001
        )
    );
