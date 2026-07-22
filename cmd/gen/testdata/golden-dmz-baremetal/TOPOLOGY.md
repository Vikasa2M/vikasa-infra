# Topology Diagram: exdot

> **Generated** by vikasa-infra/cmd/gen — regenerated on every run; do not edit.

```mermaid
flowchart TD
  subgraph cl_core["cluster: core"]
    s_VIKASA_EXDOT_CENTRAL_D1_D1_0["VIKASA_EXDOT_CENTRAL_D1_D1_0<br/>js-domain: core<br/>replicas: 5<br/>tier: central<br/>maxAge: 15m"]
    s_VIKASA_EXDOT_CENTRAL_D1_D1_8["VIKASA_EXDOT_CENTRAL_D1_D1_8<br/>js-domain: core<br/>replicas: 5<br/>tier: central<br/>maxAge: 15m"]
  end
  subgraph cl_d1a["cluster: d1a"]
    s_VIKASA_EXDOT_D1_D1_0["VIKASA_EXDOT_D1_D1_0<br/>js-domain: d1a<br/>replicas: 3<br/>tier: regional<br/>maxAge: 6h"]
    s_VIKASA_EXDOT_D1_D1_8["VIKASA_EXDOT_D1_D1_8<br/>js-domain: d1a<br/>replicas: 3<br/>tier: regional<br/>maxAge: 6h"]
  end
  subgraph cl_dmz["cluster: dmz"]
    s_VIKASA_EXDOT_DMZ["VIKASA_EXDOT_DMZ<br/>js-domain: dmz<br/>replicas: 3<br/>tier: dmz<br/>maxAge: 1h"]
  end
  s_VIKASA_EXDOT_D1_D1_0 -->|source| s_VIKASA_EXDOT_CENTRAL_D1_D1_0
  s_VIKASA_EXDOT_D1_D1_8 -->|source| s_VIKASA_EXDOT_CENTRAL_D1_D1_8
  s_VIKASA_EXDOT_CENTRAL_D1_D1_8 -->|share| s_VIKASA_EXDOT_DMZ
```
