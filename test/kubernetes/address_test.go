package kubernetes

import (
	"fmt"
	"os"
	"testing"

	"github.com/coredns/coredns/plugin/test"

	"github.com/miekg/dns"
)

const namespace = "testns"

var dnsTestCasesA = []test.Case{
	{ // An A record query for an existing service should return a record
		Qname: "svc-1-a.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("svc-1-a.test-1.svc.cluster.local.    303      IN      A       10.96.0.100"),
		},
	},
	{ // An A record query for an existing headless service should return a record for each of its ipv4 endpoints
		Qname: "headless-svc.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("headless-svc.test-1.svc.cluster.local.      303    IN      A       172.17.0.254"),
			test.A("headless-svc.test-1.svc.cluster.local.      303    IN      A       172.17.0.255"),
		},
	},
	{ // An A record query for a non-existing service should return NXDOMAIN
		Qname: "bogusservice.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("cluster.local.	303	IN	SOA	ns.dns.cluster.local. hostmaster.cluster.local. 1502313310 7200 1800 86400 60"),
		},
	},
	{ // An A record query for a non-existing endpoint should return NXDOMAIN
		Qname: "bogusendpoint.svc-1-a.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("cluster.local.	303	IN	SOA	ns.dns.cluster.local. hostmaster.cluster.local. 1502313310 7200 1800 86400 60"),
		},
	},
	{ // A TXT request for dns-version should return the version of the kubernetes service discovery spec implemented
		Qname: "dns-version.cluster.local.", Qtype: dns.TypeTXT,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.TXT(`dns-version.cluster.local. 303 IN TXT "1.1.0"`),
		},
	},
	{ // An AAAA record query for an existing headless service should return a record for each of its ipv6 endpoints
		Qname: "headless-svc.test-1.svc.cluster.local.", Qtype: dns.TypeAAAA,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.AAAA("headless-svc.test-1.svc.cluster.local.      303    IN      AAAA      1234:abcd::1"),
			test.AAAA("headless-svc.test-1.svc.cluster.local.      303    IN      AAAA      1234:abcd::2"),
		},
	},
	{ // A query to a headless service with unready endpoints should return NXDOMAIN
		Qname: "svc-unready.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeNameError,
		Ns: []dns.RR{
			test.SOA("cluster.local.        303     IN      SOA     ns.dns.cluster.local. hostmaster.cluster.local. 1499347823 7200 1800 86400 30"),
		},
	},
	{ // An NS type query
		Qname: "cluster.local.", Qtype: dns.TypeNS,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.NS("cluster.local.	303	IN	NS	kube-dns.kube-system.svc.cluster.local."),
		},
		Extra: []dns.RR{
			test.A("kube-dns.kube-system.svc.cluster.local. 303 IN A 10.96.0.10"),
		},
	},
	{ // An A record query external domain with response size greater than 512 bytes should be truncated.
		Qname: "corednse2etestaks.com.",
		Qtype: dns.TypeA,
		Rcode: dns.RcodeSuccess,
	},
}

var newObjectTests = []test.Case{
	{
		Qname: "new-svc.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("new-svc.test-1.svc.cluster.local.      303    IN      A       10.96.0.222"),
		},
	},
	{
		Qname: "172-17-0-222.new-svc.test-1.svc.cluster.local.", Qtype: dns.TypeA,
		Rcode: dns.RcodeSuccess,
		Answer: []dns.RR{
			test.A("172-17-0-222.new-svc.test-1.svc.cluster.local.      303    IN      A       172.17.0.222"),
		},
	},
}

var newObjects = `apiVersion: v1
kind: Service
metadata:
  name: new-svc
  namespace: test-1
spec:
  clusterIP: 10.96.0.222
  ports:
  - name: http
    port: 80
    protocol: TCP
---
kind: Endpoints
apiVersion: v1
metadata:
  name: new-svc
  namespace: test-1
subsets:
  - addresses:
      - ip: 172.17.0.222
    ports:
      - port: 80
        name: http
        protocol: TCP
`

func TestKubernetesA(t *testing.T) {

	rmFunc, upstream, _ := UpstreamServer(t, "example.net", ExampleNet)
	defer upstream.Stop()
	defer rmFunc()

	corefile := `    .:53 {
        errors
        ready
        health {
          lameduck 5s
        }
        kubernetes cluster.local in-addr.arpa ip6.arpa {
          pods insecure
          fallthrough in-addr.arpa ip6.arpa
          ttl 30
        }
        prometheus :9153
        forward . 168.63.129.16
        cache 30
        loop
        reload
        loadbalance
        import custom/*.override
    }
`

	err := LoadCorefile(corefile)
	if err != nil {
		t.Fatalf("Could not load corefile: %s", err)
	}
	testCases := dnsTestCasesA
	namespace := "test-1"
	err = StartClientPod(namespace)
	if err != nil {
		t.Fatalf("failed to start client pod: %s", err)
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s %s", tc.Qname, dns.TypeToString[tc.Qtype]), func(t *testing.T) {

			var res *dns.Msg
			var err error

			if tc.Qname == "corednse2etestaks.com." {
				// Calling this function defined in test/kubernetes/tool.go which uses Noedns flag in the dig command.
				res, err = DoIntegrationTestWithNoEdns(tc, namespace, "10.96.0.10")
				if err != nil {
					t.Errorf(err.Error())
				}
				if res.Rcode != tc.Rcode {
					t.Errorf("rcode is %q, expected %q", dns.RcodeToString[res.Rcode], dns.RcodeToString[tc.Rcode])
				}
				if res.Truncated != true {
					t.Errorf("CoreDNS test : "+tc.Qname+" -tc bit is %v, expected %v", res.Truncated, true)
				}
				return
			}

			res, err = DoIntegrationTest(tc, namespace)

			if err != nil {
				t.Errorf(err.Error())
			}
			test.CNAMEOrder(res)

			if err := test.SortAndCheck(res, tc); err != nil {
				t.Error(err)
			}
			if t.Failed() {
				t.Errorf("coredns log: %s", CorednsLogs())
			}
		})
	}

	newObjectsFile, rmFunc, err := test.TempFile(os.TempDir(), newObjects)
	defer rmFunc()
	if err != nil {
		t.Fatalf("could not create file to add service/endpoint: %s", err)
	}

	_, err = Kubectl("apply -f " + newObjectsFile)
	if err != nil {
		t.Fatalf("could not add service/endpoint via kubectl: %s", err)
	}

	for _, tc := range newObjectTests {
		t.Run("New Object "+tc.Qname, func(t *testing.T) {
			res, err := DoIntegrationTest(tc, namespace)
			if err != nil {
				t.Errorf(err.Error())
			}
			test.CNAMEOrder(res)
			if err := test.SortAndCheck(res, tc); err != nil {
				t.Error(err)
			}
			if t.Failed() {
				t.Errorf("coredns log: %s", CorednsLogs())
			}
		})
	}

	_, err = Kubectl("-n test-1 delete service new-svc")
	if err != nil {
		t.Fatalf("could not add service/endpoint via kubectl: %s", err)
	}
}
