package app

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/crazy-max/ddns-route53/internal/config"
	"github.com/crazy-max/ddns-route53/internal/utl"
	"github.com/rs/zerolog/log"
)

// Client represents an active ddns-route53 object
type Client struct {
	cfg      *config.Configuration
	loc      *time.Location
	r53      *route53.Route53
	lastIPv4 string
	lastIPv6 string
	locker   uint32
}

// New creates new ddns-route53 instance
func New(cfg *config.Configuration, loc *time.Location) (*Client, error) {
	// Static credentials
	creds := credentials.NewStaticCredentials(
		cfg.Credentials.AccessKeyID,
		cfg.Credentials.SecretAccessKey,
		"",
	)

	// AWS SDK session
	sess, err := session.NewSession()
	if err != nil {
		return nil, err
	}

	return &Client{
		cfg: cfg,
		loc: loc,
		r53: route53.New(sess, &aws.Config{Credentials: creds}),
	}, nil
}

// Run starts ddns-route53 process
func (c *Client) Run() {
	if !atomic.CompareAndSwapUint32(&c.locker, 0, 1) {
		log.Warn().Msg("Already running")
		return
	}
	defer atomic.StoreUint32(&c.locker, 0)

	// Get current WAN IP
	wanIPv4, err := utl.WanIPv4()
	if err != nil {
		log.Warn().Err(err).Msg("Cannot get WAN IPv4 address")
	} else {
		log.Info().Msgf("Current WAN IPv4: %s", wanIPv4)
	}

	wanIPv6, err := utl.WanIPv6()
	if err != nil {
		log.Warn().Err(err).Msg("Cannot get WAN IPv6 address")
	} else {
		log.Info().Msgf("Current WAN IPv6: %s", wanIPv6)
	}

	// Skip if last IP is identical
	if wanIPv4 == c.lastIPv4 && wanIPv6 == c.lastIPv6 {
		log.Info().Msg("WAN IPv4 and IPv6 address are the same. Skipping...")
		return
	}

	// Create Route53 changes
	r53Changes := make([]*route53.Change, len(c.cfg.Route53.RecordsSet))
	for i, rs := range c.cfg.Route53.RecordsSet {
		if wanIPv4 == "" && rs.Type == route53.RRTypeA {
			log.Error().Msgf("No WAN IPv4 address available to update %s record", rs.Name)
			continue
		} else if wanIPv6 == "" && rs.Type == route53.RRTypeAaaa {
			log.Error().Msgf("No WAN IPv6 address available to update %s record", rs.Name)
			continue
		}
		wanIP := wanIPv4
		if rs.Type == route53.RRTypeAaaa {
			wanIP = wanIPv6
		}
		r53Changes[i] = &route53.Change{
			Action: aws.String("UPSERT"),
			ResourceRecordSet: &route53.ResourceRecordSet{
				Name:            aws.String(rs.Name),
				Type:            aws.String(rs.Type),
				TTL:             aws.Int64(rs.TTL),
				ResourceRecords: []*route53.ResourceRecord{{Value: aws.String(wanIP)}},
			},
		}
	}

	// Check changes
	if len(r53Changes) == 0 {
		log.Warn().Msgf("No Route53 record set to update. Skipping...")
		return
	}

	// Create resource records
	resRS := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Comment: aws.String(fmt.Sprintf("Updated by %s %s at %s",
				c.cfg.App.Name,
				c.cfg.App.Version,
				time.Now().In(c.loc).Format("2006-01-02 15:04:05"),
			)),
			Changes: r53Changes,
		},
		HostedZoneId: aws.String(c.cfg.Route53.HostedZoneID),
	}

	// Update records
	resp, err := c.r53.ChangeResourceRecordSets(resRS)
	if err != nil {
		log.Error().Err(err).Msg("Cannot update records set")
	}
	log.Info().Interface("changes", resp).Msgf("%d records set updated", len(c.cfg.Route53.RecordsSet))
}
