import Foundation
import ProductCore
import SyncCore
import Testing

@Suite struct AdaptivePlanTests {
  let plan = try! JSON.decode(Data(#"""
    {"planID":"p","config":{"version":"training_config/v2","startDate":"2026-11-16","availableDays":[2,4,6,7],"longRunDay":7,
     "goal":{"type":"running","date":"2027-02-07","distanceM":21097.5,"targetTimeS":6300}},
     "effective":{"mode":"race","asOf":"2026-11-18","windowStart":"2026-11-16",
      "goalStatus":{"date":"2027-02-07","distanceM":21097.5,"category":"half","phase":"base","daysToRace":81,"prepStartDate":"2026-10-26",
        "targetTimeS":6300,"predictedTimeS":6500,"gapRatio":0.031,"assessment":"ambitious","racePaceSPerKM":299,"completed":false,"reasonCodes":[]},
      "weeks":[
       {"index":0,"startDate":"2026-11-16","detail":"current","phase":"base","state":"start","levelS":12000,"budgetS":12000,"plannedS":12000,"actualS":3000,"hardFocus":["tempo"],"reasonCodes":["week_state_start","phase_base"]},
       {"index":1,"startDate":"2026-11-23","detail":"next","phase":"build","state":"progress","levelS":12720,"budgetS":12720,"plannedS":12600,"reasonCodes":[]},
       {"index":2,"startDate":"2026-11-30","detail":"outline","phase":"build","state":"projected","levelS":13500,"budgetS":13500,"plannedS":13500,"reasonCodes":[]}],
      "sessions":[
       {"sessionID":"a","slot":1,"date":"2026-11-17","type":"TEMPO","focus":"threshold","purpose":"lactate_threshold","durationS":3000,"outcome":"missed","reasonCodes":[]},
       {"sessionID":"b","slot":3,"date":"2026-11-19","type":"TEMPO","focus":"threshold","purpose":"lactate_threshold","durationS":3000,
        "changedFrom":{"date":"2026-11-17","type":"EASY","durationS":2700,"reasonCode":"missed_key_session_moved"},"reasonCodes":[]},
       {"sessionID":"c","slot":6,"date":"2026-11-22","type":"LONG","durationS":3600,"reasonCodes":[]}]}}
    """#.utf8))

  @Test func readsWeeksGoalAndSessions() {
    let a = AdaptivePlan(plan: plan)
    #expect(a.isAdaptive && a.isRace && a.hasConfiguredGoal)
    #expect(a.current?.state == "start" && a.current?.budgetMinutes == 200 && a.current?.actualMinutes == 50)
    #expect(a.current.map { abs($0.progress - 0.25) < 0.001 } == true)
    #expect(a.next?.phase == "build" && a.outline.count == 1)
    #expect(a.goal?.assessment == "ambitious" && a.goal?.daysToRace == 81 && a.goal?.racePace == 299)
    let sessions = plan["effective"]["sessions"].list.map(ProductSession.init)
    #expect(sessions[0].title == "阈值跑" && sessions[0].outcome == "missed" && !sessions[0].adapted)
    #expect(sessions[1].adapted && sessions[1].changedFrom.string("reasonCode") == "missed_key_session_moved")
    // Without a focus the legacy title stays.
    #expect(sessions[2].title == "长距离跑")
    #expect(AdaptivePlanText.purpose(sessions[0].purpose) == "提升乳酸阈")
  }

  @Test func legacyPlanIsNotAdaptive() {
    let legacy = JSON.object(["config": .object(["startDate": .string("2026-11-16"), "weeks": .int(8)]), "effective": .object([:])])
    let a = AdaptivePlan(plan: legacy)
    #expect(!a.isAdaptive && a.goal == nil && a.weeks.isEmpty)
  }

  @Test func buildsV2ConfigWithOptionalGoal() throws {
    let fitness = AdaptivePlanConfig.make(monday: "2026-11-16", days: [7, 2, 4], longDay: 7, goal: nil, baselineMinutes: nil)
    #expect(fitness.string("version") == "training_config/v2")
    #expect(fitness["availableDays"].list.compactMap(\.integer) == [2, 4, 7])
    #expect(fitness["goal"] == .null && fitness["weeks"] == .null && fitness["baselineWeeklyS"] == .null)
    let race = AdaptivePlanConfig.make(monday: "2026-11-16", days: [2, 7], longDay: 7,
      goal: .init(distanceM: 21097.5, date: "2027-02-07", targetTimeS: 6300), baselineMinutes: 150)
    #expect(race["goal"].number("distanceM") == 21097.5 && race["goal"].number("targetTimeS") == 6300)
    #expect(race.number("baselineWeeklyS") == 9000)
  }

  @Test func validatesFormInput() {
    #expect(AdaptivePlanConfig.problem(days: [7], longDay: 7, goal: nil, today: "2026-11-16", baselineMinutes: nil) != nil)
    #expect(AdaptivePlanConfig.problem(days: [2, 4], longDay: 7, goal: nil, today: "2026-11-16", baselineMinutes: nil) != nil)
    #expect(AdaptivePlanConfig.problem(days: [2, 7], longDay: 7, goal: .init(distanceM: 5000, date: "2026-11-01", targetTimeS: nil), today: "2026-11-16", baselineMinutes: nil) != nil)
    #expect(AdaptivePlanConfig.problem(days: [2, 7], longDay: 7, goal: .init(distanceM: 5000, date: "2026-12-01", targetTimeS: nil), today: "2026-11-16", baselineMinutes: 120) == nil)
  }

  @Test func parsesRaceTimes() {
    #expect(AdaptivePlanText.seconds("1:45:00") == 6300)
    #expect(AdaptivePlanText.seconds("45:30") == 2730)
    #expect(AdaptivePlanText.seconds("1:75:00") == nil)
    #expect(AdaptivePlanText.seconds("abc") == nil)
    #expect(AdaptivePlanText.clock(6300) == "1:45:00" && AdaptivePlanText.clock(2730) == "45:30")
  }
}
