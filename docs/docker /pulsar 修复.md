  ┌───────────────────────────────────────────────────┐
│ docker compose -f docker-compose.yml stop pulsar  │
   │ docker rm im-pulsar                               │
 │ docker volume rm im_im-pulsardata                 │
   │ docker compose -f docker-compose.yml up -d pulsar │
   └───────────────────────────────────────────────────┘