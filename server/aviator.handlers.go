package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/thedivinez/go-libs/messaging"
	"github.com/thedivinez/go-libs/services/auth"
	"github.com/thedivinez/go-libs/services/aviator"
	"github.com/thedivinez/go-libs/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (server *Server) Subscribe(ctx context.Context, req *aviator.SubscribeRequest) (*aviator.SubscribeResponse, error) {
	currentSettings := &aviator.PlaneSettings{}
	if err := server.db.FindOne(CLIENTS_COLLECTION, bson.M{"orgId": req.OrgID}, currentSettings); err != nil {
		currentSettings.MaxDemoStake = 1
		currentSettings.OrgID = req.OrgID
		currentSettings.MinTotalBets = 100
		currentSettings.MaxTotalBets = 1000
		currentSettings.MinDemoRiskAmount = 10
		currentSettings.MaxDemoRiskAmount = 100
		currentSettings.MaxRiskPercentage = 1.5
		currentSettings.MinRiskPercentage = 0.5
		currentSettings.MaxMultiplierShift = 1.4
		currentSettings.LisenseExpiration = utils.CalculateLisenseExpiration(time.Now(), req.Package, req.Duration)
		if _, err := server.db.InsertOne(CLIENTS_COLLECTION, currentSettings); err != nil {
			return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to insert plane settings").WithInternal(err)
		}
	} else {
		currentExp := time.Unix(currentSettings.LisenseExpiration, 0)
		currentSettings.LisenseExpiration = utils.CalculateLisenseExpiration(currentExp, req.Package, req.Duration)
		if err := server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": req.OrgID}, bson.M{"$set": bson.M{"lisenseExpiration": currentSettings.LisenseExpiration}}); err != nil {
			return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to update plane settings").WithInternal(err)
		}
		server.initializePlane(req.OrgID)
	}
	return &aviator.SubscribeResponse{Settings: currentSettings, Message: "subscription has been updated"}, nil
}

func (server *Server) GetPlaneSettings(ctx context.Context, req *aviator.GetPlaneSettingsRequest) (*aviator.PlaneSettings, error) {
	currentSettings := &aviator.PlaneSettings{}
	if err := server.db.FindOne(CLIENTS_COLLECTION, bson.M{"orgId": req.OrgID}, currentSettings); err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "plane settings not found").WithInternal(err)
	}
	return currentSettings, nil
}

func (server *Server) UpdatePlaneSettings(ctx context.Context, req *aviator.PlaneSettings) (*aviator.UpdatePlaneSettingsResponse, error) {
	if err := server.db.UpdateOne(CLIENTS_COLLECTION, bson.M{"orgId": req.OrgID}, bson.M{"$set": req}); err != nil {
		return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to update plane settings").WithInternal(err)
	}
	settings := &aviator.PlaneSettings{}
	if err := server.db.FindOne(CLIENTS_COLLECTION, bson.M{"orgId": req.OrgID}, settings); err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "plane settings not found").WithInternal(err)
	}
	return &aviator.UpdatePlaneSettingsResponse{Message: "settings updated", Settings: settings}, nil
}

func (server *Server) PlaneCashout(ctx context.Context, req *aviator.PlaneBet) (*aviator.PlaneCashoutResponse, error) {
	flightRedisKey := planeflightRedisKey(req.OrgID, req.FlightID)
	flightBetsRedisKey := flightBetsRedisKey(req.OrgID, req.FlightID)
	path := fmt.Sprintf("$.[?(@.id=='%s' && @.flightId=='%s')]", req.BetId, req.FlightID)
	if err := server.redis.Read(flightBetsRedisKey, path, &req); err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "bet does not exist").WithInternal(err)
	}

	flight, err := server.getFlightById(req.OrgID, req.FlightID)
	if err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "flight does not exist").WithInternal(err)
	}
	req.Status = "closed"
	req.Payout = flight.Multiplier * req.Stake
	server.auth.AddToAccountBalance(ctx, &auth.AddToAccountBalanceRequest{
		OrgID:  req.OrgID,
		Amount: req.Payout,
		UserId: req.UserID,
		Target: req.Account,
		Source: server.config.ServiceName,
	})
	if req.Account == "live" {
		server.redis.Client.JSONNumIncrBy(context.Background(), flightRedisKey, "$.profitBlown", req.Payout)
	}
	if err := server.redis.Client.JSONDel(context.Background(), flightBetsRedisKey, path).Err(); err != nil {
		return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to place bet").WithInternal(err)
	}
	req.Status = "cashedout"
	go server.db.InsertOne(BETS_COLLECTION, req)
	server.messaging.Send(messaging.EventMessage{Event: "flightbet:update", Room: req.UserID, OrgId: req.OrgID, Message: req})
	return &aviator.PlaneCashoutResponse{Message: "bet cashed out"}, nil
}

func (server *Server) CancelPlaneBet(ctx context.Context, req *aviator.PlaneBet) (*aviator.CancelPlaneBetResponse, error) {
	flight, err := server.getFlightById(req.OrgID, req.FlightID)
	if err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "flight does not exist").WithInternal(err)
	}

	flightBetsRedisKey := flightBetsRedisKey(flight.OrgID, flight.ID)
	path := fmt.Sprintf("$.[?(@.id=='%s' && @.flightId=='%s' && @.status!='closed')]", req.BetId, flight.ID)
	if err := server.redis.Read(flightBetsRedisKey, path, req); err != nil {
		return nil, utils.NewServiceError(http.StatusNotFound, "bet does not exist in this flight").WithInternal(err)
	}

	if err := server.redis.Client.JSONDel(context.Background(), flightBetsRedisKey, path).Err(); err != nil {
		return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to cancel bet").WithInternal(err)
	}

	req.Status = "canceled"
	server.auth.AddToAccountBalance(ctx, &auth.AddToAccountBalanceRequest{
		Amount: req.Stake,
		OrgID:  req.OrgID,
		UserId: req.UserID,
		Target: req.Account,
		Source: server.config.ServiceName,
	})
	server.messaging.Send(messaging.EventMessage{Event: "flightbet:update", Room: req.UserID, OrgId: req.OrgID, Message: req})
	return &aviator.CancelPlaneBetResponse{Message: "bet canceled"}, nil
}

func (server *Server) PlacePlaneBet(ctx context.Context, bet *aviator.PlaneBet) (*aviator.PlacePlaneBetResponse, error) {
	if user, err := server.auth.FindUserById(ctx, &auth.FindUserByIdRequest{UserId: bet.UserID}); err == nil {
		flight, err := server.getFlightByState(user.OrgID, STATE_PENDING)
		if err != nil {
			if loadingFlight, err := server.getFlightByState(user.OrgID, STATE_LOADING); err != nil {
				return nil, utils.NewServiceError(http.StatusNotFound, "there are no flights ready for betting").WithInternal(err)
			} else {
				flight = loadingFlight
			}
		}
		flightBetsRedisKey := flightBetsRedisKey(flight.OrgID, flight.ID)
		path := fmt.Sprintf("$.[?(@.flightId=='%s' && @.userId=='%s' && @.side=='%s')]", flight.ID, user.ID, bet.Side)
		if err := server.redis.Read(flightBetsRedisKey, path, &aviator.PlaneBet{}); err != nil {
			balance := server.getCurrentUserBalance(user)
			if balance < bet.Stake {
				return nil, utils.NewServiceError(http.StatusForbidden, "insufficient account balance")
			}
			bet.Status = "waiting"
			if flight.State == STATE_LOADING {
				bet.Status = "open"
			}
			bet.UserID = user.ID
			bet.OrgID = user.OrgID
			bet.FlightID = flight.ID
			bet.DateCreated = time.Now().Unix()
			bet.BetId = primitive.NewObjectID().Hex()
			if _, err := server.redis.Client.JSONArrAppend(context.Background(), flightBetsRedisKey, "$", bet).Result(); err != nil {
				return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to place bet").WithInternal(err)
			}
			server.auth.AddToAccountBalance(ctx, &auth.AddToAccountBalanceRequest{
				UserId: bet.UserID,
				Amount: -bet.Stake,
				Target: bet.Account,
				OrgID:  user.OrgID,
				Source: server.config.ServiceName,
			})
			server.messaging.Send(messaging.EventMessage{Event: "flightbet:update", Room: bet.UserID, OrgId: bet.OrgID, Message: bet})
			return &aviator.PlacePlaneBetResponse{Message: "bet has been created", Bet: bet}, nil
		}
		return nil, utils.NewServiceError(http.StatusForbidden, fmt.Sprintf("you have already placed a %s side bet for this flight", bet.Side))
	}
	return nil, utils.NewServiceError(http.StatusForbidden, "")
}

func (server *Server) GetPlaneBets(ctx context.Context, req *aviator.GetPlaneBetsRequest) (*aviator.GetPlaneBetsResponse, error) {
	bets := []*aviator.PlaneBet{}
	if err := server.db.GetPage(BETS_COLLECTION, bson.M{"userId": req.UserID}, req.Page, req.Limit, 1, &bets); err != nil {
		return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to get plane bets").WithInternal(err)
	}
	return &aviator.GetPlaneBetsResponse{Bets: bets}, nil
}

func (server *Server) GetPlaneHistory(ctx context.Context, req *aviator.GetPlaneHistoryRequest) (*aviator.GetPlaneHistoryResponse, error) {
	if explosionHistory, err := server.redis.Client.LRange(ctx, fmt.Sprintf("%s-plane-history", req.OrgID), 0, -1).Result(); err != nil {
		return nil, utils.NewServiceError(http.StatusInternalServerError, "failed to get plane history").WithInternal(err)
	} else {
		return &aviator.GetPlaneHistoryResponse{History: explosionHistory}, nil
	}
}

func (server *Server) GetActiveBets(ctx context.Context, req *aviator.GetActiveBetsRequest) (*aviator.GetActiveBetsResponse, error) {
	bets := []*aviator.PlaneBet{}
	if flightsBetStores, err := server.redis.Client.Keys(context.TODO(), fmt.Sprintf("%s-flight:bets-*", req.OrgID)).Result(); err == nil {
		for _, betStore := range flightsBetStores {
			betsInOneFlight := []*aviator.PlaneBet{}
			path := fmt.Sprintf("$.[?(@.userId=='%s')]", req.UserID)
			if err := server.redis.Read(betStore, path, &betsInOneFlight); err == nil {
				for _, bet := range betsInOneFlight {
					if flight, err := server.getFlightById(req.OrgID, bet.FlightID); err == nil {
						if flight.State == STATE_FLYING {
							bet.Status = "closed"
						}
						bets = append(bets, bet)
					}
				}
			}
		}
	}
	return &aviator.GetActiveBetsResponse{Bets: bets}, nil
}
