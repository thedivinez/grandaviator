package server

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/pkg/errors"
	"github.com/thedivinez/go-libs/messaging"
	"github.com/thedivinez/go-libs/services/auth"
	"github.com/thedivinez/go-libs/services/aviator"
	"github.com/thedivinez/go-libs/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func planeflightRedisKey(orgId, flightId string) string {
	return fmt.Sprintf("%s-plane:flight-%s", orgId, flightId)
}

func flightBetsRedisKey(orgId, flightId string) string {
	return fmt.Sprintf("%s-flight:bets-%s", orgId, flightId)
}

func (server *Server) getCurrentUserBalance(user *auth.User) float64 {
	balance := user.DemoBalance
	if user.CurrentAccount == "live" {
		balance = user.LiveBalance
	}
	return balance
}

func (server *Server) getFlightById(orgId, flightId string) (*aviator.Flight, error) {
	flight := aviator.Flight{}
	if err := server.redis.Read(fmt.Sprintf("%s-plane:flight-%s", orgId, flightId), "$", &flight); err != nil {
		return nil, err
	}
	return &flight, nil
}

func (server *Server) getPlaneSettings(orgId string) *aviator.PlaneSettings {
	balances := aviator.PlaneSettings{}
	server.db.FindOne(CLIENTS_COLLECTION, bson.M{"orgId": orgId}, &balances)
	return &balances
}

func (server *Server) getPlaneBets(orgId, flightId, filter string) []aviator.PlaneBet {
	bets := []aviator.PlaneBet{}
	if err := server.redis.Read(flightBetsRedisKey(orgId, flightId), filter, &bets); err != nil {
		server.log.Err(err).Msg("failed to read bets")
	}
	return bets
}

func (server *Server) getFlightByState(orgId, state string) (*aviator.Flight, error) {
	ctx := context.Background()
	for iter := server.redis.Scan(ctx, 0, fmt.Sprintf("%s-plane:flight-*", orgId), 0); iter.Next(ctx); {
		flight := aviator.Flight{}
		if err := server.redis.Read(iter.Val(), "$", &flight); err != nil {
			return nil, err
		}
		if flight.State == state {
			return &flight, nil
		}
	}
	return nil, errors.WithStack(errors.New("no flight found"))
}

func (server *Server) broadcastFlightState(flight *aviator.Flight) {
	server.messaging.Send(messaging.EventMessage{
		Room:    "plane",
		Service: "aviator",
		OrgId:   flight.OrgID,
		Event:   "flight:state",
		Message: &aviator.FlightState{
			State:       flight.State,
			ID:          flight.ID,
			TotalBets:   flight.TotalBets,
			LeaderBoard: flight.LeaderBoard,
			Multiplier:  fmt.Sprintf("%.2fx", flight.Multiplier-0.01),
		},
	})
}

func generateName() string {
	alphabet := []rune("abcdefghijklmnopqrstuvwxyz")
	lastLetter := string(alphabet[utils.RandInt(0, len(alphabet))])
	firstLetter := string(alphabet[utils.RandInt(0, len(alphabet))])
	return fmt.Sprintf("%s***%s", firstLetter, lastLetter)
}

func (server *Server) generateLeaderBoard() []*aviator.FlightLeaderBoard {
	leaderBoard := []*aviator.FlightLeaderBoard{}
	for i := 0; i < 19; i++ {
		name := generateName()
		for slices.ContainsFunc(leaderBoard, func(x *aviator.FlightLeaderBoard) bool {
			return x.Name == name
		}) {
			name = generateName()
		}
		leaderBoard = append(leaderBoard, &aviator.FlightLeaderBoard{
			PayOut:    0,
			Name:      name,
			CashedOut: false,
			Stake:     utils.RandFloat(1.0, 100.0),
		})
	}
	return leaderBoard
}

func (server *Server) createNextFlight(orgID string) *aviator.Flight {
	if pendingFlight, err := server.getFlightByState(orgID, STATE_PENDING); err == nil {
		return pendingFlight
	}
	if loadingFlight, err := server.getFlightByState(orgID, STATE_LOADING); err == nil {
		return loadingFlight
	}
	flight := &aviator.Flight{
		Multiplier:  1.0,
		OrgID:       orgID,
		State:       STATE_PENDING,
		DateCreated: time.Now().Unix(),
		LeaderBoard: []*aviator.FlightLeaderBoard{},
		ID:          primitive.NewObjectID().Hex(),
	}
	if err := server.redis.Write(planeflightRedisKey(orgID, flight.ID), "$", flight); err != nil {
		server.log.Err(err).Msg("failed to write flight")
	}
	if err := server.redis.Write(flightBetsRedisKey(orgID, flight.ID), "$", []aviator.PlaneBet{}); err != nil {
		server.log.Err(err).Msg("failed to initialize flight bets")
	}
	if err := server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": orgID}, bson.M{"$inc": bson.M{"amountToRisk": -flight.Risk}}); err != nil {
		server.log.Err(err).Msg("failed to write flight")
	}
	return flight
}

func (server *Server) initializePlane(orgID string) {
	go func() {
		flights := 0
		for {
			settings := server.getPlaneSettings(orgID)
			if time.Unix(settings.LisenseExpiration, 0).After(time.Now()) {
				server.log.Log().Msg("lisense expired")
				server.messaging.Send(messaging.EventMessage{
					OrgId:   orgID,
					Room:    "admin",
					Service: "aviator",
					Event:   "license:update",
					Message: "your aviator license has expired",
				})
				return
			}
			flight, err := server.getFlightByState(orgID, STATE_FLYING)
			if err != nil {
				flight = server.createNextFlight(orgID)
			}
			totalStakes := 0.0
			server.log.Log().Msg("starting flight")
			flightRedisKey := planeflightRedisKey(orgID, flight.ID)
			if flight.State != STATE_FLYING {
				for timeBeforeStart := range utils.StartCountDown(time.Now(), time.Now().Add(time.Second*14)) {
					if timeBeforeStart.T <= 0 {
						flights++
						flight.State = STATE_FLYING
						if bets := server.getPlaneBets(orgID, flight.ID, "$.[?(@.account=='live')]"); len(bets) > 0 {
							for idx := range bets {
								totalStakes += bets[idx].Stake
							}
							riskAmount := utils.RandFloat(settings.MinRiskPercentage, settings.MaxRiskPercentage) * totalStakes
							if riskAmount <= settings.AmountToRisk {
								server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": orgID}, bson.M{"$inc": bson.M{"amountToRisk": -riskAmount}})
							} else if riskAmount <= settings.ReservedBalance {
								server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": orgID}, bson.M{"$inc": bson.M{"reservedBalance": -riskAmount}})
							} else if riskAmount <= (settings.ReservedBalance + settings.AmountToRisk) {
								combinedBalance := settings.ReservedBalance + settings.AmountToRisk
								server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": orgID}, bson.M{"$set": bson.M{"amountToRisk": combinedBalance - riskAmount, "reservedBalance": 0.0}})
							} else {
								riskAmount = 0
							}
							flight.Risk = totalStakes + riskAmount
							server.redis.Write(flightRedisKey, "$.risk", flight.Risk)
						} else {
							flight.Risk = utils.RandFloat(settings.MinDemoRiskAmount, settings.MaxDemoRiskAmount)
						}
						if err := server.redis.Write(flightRedisKey, "$.state", flight.State); err != nil {
							server.log.Err(err).Msg("failed to update flight state")
						}
						server.broadcastFlightState(flight)
						server.createNextFlight(orgID)
						break
					}
					flight.State = STATE_LOADING
					if err := server.redis.Write(flightRedisKey, "$.state", flight.State); err != nil {
						server.log.Err(err).Msg("failed to update flight state")
					}

					server.broadcastFlightState(flight)
				}
			}

			flight.LeaderBoard = server.generateLeaderBoard()
			flight.TotalBets = int64(utils.RandInt(int(settings.MinTotalBets), int(settings.MaxTotalBets)))
			for range time.NewTicker(time.Millisecond * 120).C {
				flightRiskUsed := 0.0
				flight.Multiplier += 0.01
				if flight.Multiplier >= 3.0 {
					flight.Multiplier += utils.RandFloat(0.01, settings.MaxMultiplierShift)
				}

				if flights >= int(settings.AutoExplodeAfter) {
					flights = 0
					flightRiskUsed = flight.Risk
				}

				if flightRiskUsed < flight.Risk {
					bets := server.getPlaneBets(orgID, flight.ID, "$")
					if len(bets) == 0 || totalStakes == 0 {
						flightRiskUsed += utils.RandFloat(settings.MaxDemoStake, settings.MaxDemoStake) * flight.Multiplier
					}
					for idx := range bets {
						bets[idx].Status = "closed"
						payout := bets[idx].Stake * flight.Multiplier
						if bets[idx].Account == "live" {
							flightRiskUsed += payout
						}
						if flightRiskUsed < flight.Risk {
							bets[idx].Payout = payout
						} else {
							break
						}
						server.messaging.Send(messaging.EventMessage{
							Service: "aviator",
							Message: &bets[idx],
							OrgId:   bets[idx].OrgID,
							Room:    bets[idx].UserID,
							Event:   "flightbet:update",
						})
					}

					server.redis.Write(flightRedisKey, "$.multiplier", flight.Multiplier)

					cashOutTarget := utils.RandInt(0, len(flight.LeaderBoard)-1)
					if flight.Multiplier >= utils.RandFloat(1.03, 1.10*flight.Multiplier) {
						if !flight.LeaderBoard[cashOutTarget].CashedOut {
							flight.LeaderBoard[cashOutTarget].Multiplier = fmt.Sprintf("%.2fx", flight.Multiplier-0.01)
							flight.LeaderBoard[cashOutTarget].CashedOut = cashOutTarget == utils.RandInt(0, len(flight.LeaderBoard)-1)
							flight.LeaderBoard[cashOutTarget].PayOut = flight.LeaderBoard[cashOutTarget].Stake * flight.Multiplier
						}
					}

					server.broadcastFlightState(flight)

				}

				if flightRiskUsed >= flight.Risk {
					ctx := context.Background()
					flight.State = STATE_EXPLODED
					server.broadcastFlightState(flight)
					planeHistoryRedisKey := fmt.Sprintf("%s-plane-history", flight.OrgID)
					if flightsCount, err := server.redis.Client.LLen(ctx, planeHistoryRedisKey).Result(); err == nil && flightsCount >= 20 {
						server.redis.Client.RPop(ctx, planeHistoryRedisKey)
					}
					server.redis.Client.LPush(ctx, planeHistoryRedisKey, fmt.Sprintf("%.2fx", flight.Multiplier-0.01))
					if currentFlight, err := server.getFlightById(orgID, flight.ID); err == nil {
						profitOnFlight := (currentFlight.Risk - currentFlight.ProfitBlown) * .5
						server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": orgID}, bson.M{"$inc": bson.M{"reservedBalance": profitOnFlight, "amountToRisk": profitOnFlight}})
						if flightbets := server.getPlaneBets(orgID, flight.ID, "$"); len(flightbets) > 0 {
							server.db.InsertOne(FLIGHTS_COLLECTION, currentFlight)
							if err := server.db.InsertMany(BETS_COLLECTION, flightbets); err != nil {
								server.log.Err(err).Msg("failed to insert bets to db")
							}
						}
					}
					server.redis.Client.Del(ctx, flightRedisKey)
					server.redis.Client.Del(ctx, flightBetsRedisKey(orgID, flight.ID))
					time.Sleep(time.Second * 4)
					break
				}
			}
		}
	}()
}
