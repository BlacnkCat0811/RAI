-- +goose Up
-- A separate immutable revision for the eight-full-week active-week median.
-- Migration 60 and coach_parameters/v2 remain available for exact replay.
INSERT INTO training_parameter_sets(version,parameters) VALUES ('coach_parameters/v3','{"version":"coach_parameters/v3","minSessionS":1800,"recoverySessionS":1800,"maxEasyS":4500,"maxWeekS":36000,"coldStartWeekS":3600,"lowBaseWeekS":5400,"twoHardWeekS":9000,"twoHardDays":4,"lowFloorFactor":1.3,"floorWeeksExit":2,"longShare":0.3,"longMinS":3600,"longMaxFitnessS":7200,"longMax5kS":5400,"longMax10kS":6000,"longMaxHalfS":7200,"longMaxMarathonS":10800,"progression":0.06,"progressionCap":0.1,"recoveryFactor":0.8,"progressStreak":3,"completeProgress":0.85,"completeHold":0.6,"acwrHold":1.3,"acwrRecover":1.5,"returnGapDays":10,"readinessLow":60,"readinessRest":40,"readinessWeekLow":50,"hardSpacingDays":2,"overBudget":1.1,"extraVolumeS":600,"doneRatio":0.5,"focusLowMin":0.7,"focusHighMin":0.15,"focusAnaerobicMin":0.03,"focusMinRuns":3,"goalOnTrack":0.03,"goalAmbitious":0.08,"historyDays":63,"windowWeeks":4,"baselineWeeks":8}');

-- +goose Down
-- Immutable parameter sets and their generation history are retained.
