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
  cab_exdot_d1a_cab_001(["cabinet: exdot-d1a-cab-001"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_0
  cab_exdot_d1a_cab_002(["cabinet: exdot-d1a-cab-002"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_0
  cab_exdot_d1a_cab_003(["cabinet: exdot-d1a-cab-003"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_0
  cab_exdot_d1a_cab_004(["cabinet: exdot-d1a-cab-004"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_0
  cab_exdot_d1a_cab_005(["cabinet: exdot-d1a-cab-005"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_0
  cab_exdot_d1a_cab_006(["cabinet: exdot-d1a-cab-006"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_8
  cab_exdot_d1a_cab_007(["cabinet: exdot-d1a-cab-007"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_8
  cab_exdot_d1a_cab_008(["cabinet: exdot-d1a-cab-008"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_8
  cab_exdot_d1a_cab_009(["cabinet: exdot-d1a-cab-009"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_8
  cab_exdot_d1a_cab_010(["cabinet: exdot-d1a-cab-010"]) -->|leaf| s_VIKASA_EXDOT_D1_D1_8
  s_VIKASA_EXDOT_CENTRAL_D1_D1_8 -->|share| s_VIKASA_EXDOT_DMZ
```
