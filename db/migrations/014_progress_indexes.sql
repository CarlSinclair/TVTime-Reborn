-- Up next reads every aired episode of every followed show and checks it against
-- what the person watched. These indexes hold everything that read needs, so it
-- no longer touches the table rows; the new watched index also serves the old
-- (user_id, show_id) lookups, so that one goes.
CREATE INDEX idx_episodes_progress ON episodes(show_id, season_number, episode_number, air_date);
CREATE INDEX idx_watched_progress ON watched(user_id, show_id, watched_at);
DROP INDEX idx_watched_user_show;
