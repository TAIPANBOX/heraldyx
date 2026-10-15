Feature: Quiet hours and maintenance windows hold alerts below critical, and say when they end

  @decided 2026-10-15: an operator can set a daily quiet period on their own
  clock, and can open a bounded maintenance window from the box while work is
  under way. Below critical, alerts raised in either are held and counted
  rather than mailed, and what they held is reported when the window ends. A
  critical is never held by either. A maintenance window says when it opens and
  always says when it closes, even when nothing was held. A window is at most a
  day long, and the quiet period is read in the operator's own zone.

  Scenario: an alert arrives during quiet hours and is reported when they end
    Given quiet hours run from 22:00 to 07:00 in London
    When a high alert arrives at 23:00 and nothing else happens until 07:00
    Then the alert is not mailed, and one mail at 07:00 says one alert was held
  # @test:TestQuietHoursHoldAndReportWhenTheyEnd

  Scenario: a critical is sent during quiet hours and during a maintenance window
    Given quiet hours are on, or a maintenance window is open
    When a critical alert arrives
    Then it is mailed at once and says a critical is never held
  # @test:TestACriticalIsSentDuringMaintenance

  Scenario: a maintenance window says when it opens and when it closes
    Given the operator opened a thirty minute maintenance window with a note
    When one alert arrives inside it and the window ends
    Then the opening is mailed, the alert is not, and the close says one alert was held
  # @test:TestAMaintenanceWindowSaysWhenItOpensAndWhenItCloses

  Scenario: a window that held nothing still says it closed
    Given the operator opened a ten minute maintenance window
    When the window ends with nothing held
    Then a mail still says the window closed with no alerts held
  # @test:TestAMaintenanceWithNothingHeldStillSaysItClosed

  Scenario: a window opens once and always closes
    Given a maintenance window is in force
    When the process polls again and again, then the window ends
    Then it is announced once and its close is reported once
  # @test:TestAMaintenanceWindowOpensOnceAndAlwaysCloses

  Scenario: a window holds only the source it names
    Given a maintenance window is scoped to one source
    When an alert from another source arrives during it
    Then it is mailed as usual
  # @test:TestAScopedMaintenanceHoldsOnlyItsSource

  Scenario: a window holds below critical only
    Given a maintenance window and quiet hours are both open
    When a critical and a high arrive
    Then the critical is sent and the high is held
  # @test:TestAWindowHoldsBelowCriticalAndNeverACritical

  Scenario: held alerts do not spend the hourly budget
    Given the hourly budget is nearly used up
    When many alerts are held by a window
    Then none of them is counted against the budget
  # @test:TestWindowHeldAlertsDoNotCountTowardTheCeiling

  Scenario: a held alert is released when quiet hours end
    Given an alert was held during quiet hours
    When the hours are still on, and then they end
    Then the release is owed only after they end, and says the worst severity held
  # @test:TestAQuietHoldIsReleasedWhenTheWindowEnds

  Scenario: quiet hours follow the clock change
    Given quiet hours run from 22:00 to 07:00 in London
    When the clocks go back on the 25th of October
    Then the window still covers the same wall-clock hours
  # @test:TestQuietHoursFollowTheClockChange

  Scenario: a maintenance window longer than a day is refused
    When the operator asks for a 25 hour window
    Then the command refuses and nothing is written
  # @test:TestAMaintenanceLongerThanADayIsRefused

  Scenario: quiet hours that cannot be read are off, and the box says why
    Given the quiet setting is not a clock range, or names an unknown zone
    When the process starts
    Then quiet hours are off and the start-up line says why
  # @test:TestAMalformedQuietSettingIsOffAndSaysWhy
