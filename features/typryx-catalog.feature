Feature: typryx's four event types are described honestly

  typryx is an optional add-on (agent-passport SPEC.md 6.2) that answers a
  typed question with a probability. @decided 2026-09-26: its journal moves
  onto the shared bus in all three launchers, so heraldyx now
  sees typed_answer (info), typed_unanswered (medium), typed_refused (high)
  and calibration_drift (high). Severities are typryx's own and are not
  changed here. typed_refused branches on typryx's own reason codes rather
  than mailing one generic sentence, because a cap the operator configured
  on purpose and a caller sending a malformed question want opposite
  responses. calibration_drift reports a measurement, never an enforcement,
  and names the group and the metric that crossed from the event's own data.

  Scenario: every one of the four types has a sentence
    Given typed_answer, typed_unanswered, typed_refused and calibration_drift
    When each is rendered
    Then none of them falls through to "raised an event this build does not have a description for"
  # @test:TestEveryTypryxTypeIsDescribed

  Scenario: an answered question names its template and backend
    Given a typed_answer event carrying a template, a backend and a model
    Then the mail names all three, and never the raw 64-byte template version digest
  # @test:TestTypedAnswerIsDescribedAndNamesItsTemplateAndBackend

  Scenario: an unanswered question says so without guessing why
    Given a typed_unanswered event
    Then the mail says the ask came back with no usable answer
  # @test:TestTypedUnansweredIsDescribed

  Scenario: each of typryx's own refusal reasons is named plainly
    Given a typed_refused event for each reason typryx's own code can raise
    Then the mail's wording is specific to that reason, not one generic sentence
  # @test:TestTypedRefusedNamesEachReasonPlainly

  Scenario: a refusal reason this build has never heard of stays neutral
    Given a typed_refused event carrying a reason code outside the known set
    Then the mail keeps the neutral base sentence and never repeats the raw reason string
  # @test:TestTypedRefusedUnknownReasonStaysNeutral

  Scenario: a calibration drift names its group and the metric that crossed
    Given a calibration_drift event naming a template, backend, model and one crossed bound
    Then the mail names the template, backend, model and the metric's value, and says nothing about the deployment changed
  # @test:TestCalibrationDriftNamesTheGroupAndTheMetricThatCrossed

  Scenario: both metrics are named when both cross, in a stable order
    Given a calibration_drift event whose bounds_crossed carries both max_brier and max_ece
    Then the mail names both, Brier score before ECE, every time
  # @test:TestCalibrationDriftNamesBothMetricsWhenBothCross

  Scenario: a hostile or malformed bounds_crossed never reaches the mail
    Given a calibration_drift event whose bounds_crossed is text, nil, an unrecognised bound name, or not a map at all
    Then none of it is rendered
  # @test:TestCalibrationDriftRejectsHostileBoundsCrossed

  Scenario: an unsafe value under template or backend is dropped like any other allowlisted field
    Given a typed_answer event whose template contains a line break and whose backend is 200 characters long
    Then neither value reaches the mail
  # @test:TestTypedAnswerUnsafeTemplateOrBackendIsDropped

  Scenario: the severities heraldyx renders for are typryx's own
    Given typed_answer at info, typed_unanswered at medium, typed_refused at high and calibration_drift at high
    Then every one still renders a message
  # @test:TestTypryxSeveritiesAreNotChangedByThisFile
