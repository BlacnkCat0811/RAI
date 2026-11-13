-- +goose Up
-- training_plan/v4: a rolling, adaptive plan selected by a training_config/v2
-- config. Its constants are the immutable coach_parameters/v2 set; the
-- generator refuses any other values.
INSERT INTO training_parameter_sets(version,parameters) VALUES ('coach_parameters/v2','{"version":"coach_parameters/v2","minSessionS":1800,"recoverySessionS":1800,"maxEasyS":4500,"maxWeekS":36000,"coldStartWeekS":3600,"lowBaseWeekS":5400,"twoHardWeekS":9000,"twoHardDays":4,"lowFloorFactor":1.3,"floorWeeksExit":2,"longShare":0.3,"longMinS":3600,"longMaxFitnessS":7200,"longMax5kS":5400,"longMax10kS":6000,"longMaxHalfS":7200,"longMaxMarathonS":10800,"progression":0.06,"progressionCap":0.1,"recoveryFactor":0.8,"progressStreak":3,"completeProgress":0.85,"completeHold":0.6,"acwrHold":1.3,"acwrRecover":1.5,"returnGapDays":10,"readinessLow":60,"readinessRest":40,"readinessWeekLow":50,"hardSpacingDays":2,"overBudget":1.1,"extraVolumeS":600,"doneRatio":0.5,"focusLowMin":0.7,"focusHighMin":0.15,"focusAnaerobicMin":0.03,"focusMinRuns":3,"goalOnTrack":0.03,"goalAmbitious":0.08,"historyDays":56,"windowWeeks":4}');

-- A v4 plan has no end date: its generated slots are days since the anchor
-- Monday. The original bound (16 weeks) applied to fixed-length v3 plans only.
-- +goose StatementBegin
DO $$
DECLARE c text;
BEGIN
 SELECT conname INTO c FROM pg_constraint
 WHERE conrelid='training_sessions'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%slot < 112%';
 IF c IS NULL THEN RAISE EXCEPTION 'training_sessions slot constraint not found'; END IF;
 EXECUTE format('ALTER TABLE training_sessions DROP CONSTRAINT %I', c);
END $$;
-- +goose StatementEnd
ALTER TABLE training_sessions ADD CONSTRAINT training_sessions_slot CHECK
 ((origin='generated' AND slot>=0 AND slot<36600) OR (origin='user_added' AND slot IS NULL));

-- +goose Down
-- Parameter sets and generated sessions are retained for binary rollback.
